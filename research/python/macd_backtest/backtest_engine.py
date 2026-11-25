"""Causal known20 minute-proxy account. No IO, credentials, broker or RSI."""
from fractions import Fraction as F
from .model import (SESSIONS, aggregate, macd, cross, entry_minute, guards,
                    fees, exit_reference, capacity_at_seal, require)


def fast_features(date, symbols):
    """Compute a continuous EMA once per run, retaining its full causal prefix."""
    values={};signals={};counts={'scheduled':0,'causal':0,'up':0,'down':0}
    for symbol,rec in sorted(symbols.items()):
        pairs=aggregate(date,rec['bars'].values(),rec['corporate']);v={}
        for start,end in SESSIONS:
            run=[]
            def flush():
                result=macd([b.c for b in run])
                for i,b in enumerate(run):
                    if i>=34:
                        v[b.minute+2]={'side':cross(result[i-1],result[i]),'close':b.c,
                                      'macd':result[i][0],'signal':result[i][1],
                                      'histogram':result[i][2],'run_bars':i+1}
            for m in range(start,end-1,2):
                if m not in pairs:
                    flush();run=[]
                else:run.append(pairs[m])
            flush()
            for m in range(start+68,end-1,2):
                if entry_minute(m) is None:continue
                counts['scheduled']+=1
                if m+2 in v:
                    counts['causal']+=1
                    counts['up']+=v[m+2]['side']==1
                    counts['down']+=v[m+2]['side']==-1
        values[symbol]=v
        for t,a in v.items():signals.setdefault(t,{})[symbol]=a
    return values,signals,counts


def fraction(x):return F(x)


class Book:
    def __init__(self,bp,emit=None):
        require(bp in (10,20),'FROZEN_COST')
        self.bp=bp;self.equity=F(400000);self.active=None;self.stopped=False
        self.intents=[];self.days=[];self.day=None;self.daily_net=F();self.day_open=F()
        self.reason_counts={};self.emit=emit or (lambda row:None)

    def reason(self,r):self.reason_counts[r]=self.reason_counts.get(r,0)+1

    def begin(self,date):
        self.day=date;self.day_open=self.equity;self.daily_net=F()

    def unknown(self,minute,reason):
        self.active.update(outcome='UNRESOLVED',recognized=minute,reason=reason,
                           last_settled_equity=str(self.equity),released=None)
        self.stopped=True;self.reason(reason)

    def settle(self,t,symbols):
        a=self.active
        if a is None or a['entry_due']>t-1:return
        m=t-1;b=symbols[a['symbol']]['bars'].get(m)
        if b is None or not b.good:
            self.unknown(t,'MISSING_OR_BAD_HELD_REFERENCE');return
        if a.get('entry_price') is None:
            self.unknown(t,'UNKNOWN_ENTRY_OPEN');return
        if a.get('actual_guard') is False:
            a.update(outcome='DECLINED_ACTUAL_GUARD',recognized=t,released=t)
            self.active=None;self.reason('DECLINED_ACTUAL_GUARD');return
        timed=m>=a['exit_due']
        price,why=exit_reference(b,a['stop'],a['target'],timed)
        if price is not None and why=='time_open':why=a['exit_cause']+'_open'
        entry=100 if m==a['entry_due'] else 0
        if not capacity_at_seal(b.v,entry,100 if price is not None else 0):
            self.unknown(t,'INSUFFICIENT_POSTSEAL_VOLUME');return
        if entry:a['entry_recognized']=t
        if price is not None:
            gross=F((price-a['entry_price'])*100,10000)
            cost=fees(a['entry_price'],self.bp)
            friction=F(a['entry_price']*100*self.bp,10000*10000)
            finance=cost-friction;net=gross-cost
            before=self.equity;self.equity+=net;self.daily_net+=net
            a.update(outcome='CLOSED',exit_bar=m,exit_price=price,reason=why,
                     recognized=t,released=t,gross=str(gross),friction=str(friction),
                     finance=str(finance),net=str(net),equity_before_settlement=str(before),
                     equity_after_settlement=str(self.equity))
            self.active=None;self.reason('CLOSED')

    def open(self,t,symbols):
        a=self.active
        if a is None or a['entry_due']!=t:return
        b=symbols[a['symbol']]['bars'].get(t)
        # Neither Good/H/L/V nor future availability decides entry or releases cash here.
        price=b.o if b is not None and b.o>0 else None
        a['entry_price']=price
        if price is None:a['actual_guard']=None;return
        ok,stop,target,loss,reward=guards(a['planned_close'],price,self.equity,self.bp)
        a.update(actual_guard=ok,actual_loss=str(loss),actual_reward=str(reward))
        require((stop,target)==(a['stop'],a['target']),'BRACKET_MUTATION')

    def decide(self,t,symbols,signals):
        batch=signals.get(t,{})
        if self.active is not None and batch.get(self.active['symbol'],{}).get('side')==-1:
            due=t+1
            if due<self.active['exit_due']:
                self.active['exit_due']=due;self.active['exit_cause']='macd'
        candidates=[]
        for symbol,a in sorted(batch.items()):
            if a['side']!=1 or entry_minute(t-2) is None:continue
            candidates.append(symbol)
        if not candidates:return
        if self.stopped:reason='NOT_EVALUATED_AFTER_STOP'
        elif self.active is not None:reason='OCCUPIED_UNTIL_SEAL'
        elif self.daily_net<=-self.day_open/100:reason='DAILY_LOSS_STOP'
        elif self.equity<=0:reason='NO_CAPITAL'
        else:reason='NO_PLANNED_ELIGIBLE'
        selected=None
        if reason=='NO_PLANNED_ELIGIBLE':
            for symbol in candidates:
                close=batch[symbol]['close']
                ok,stop,target,loss,reward=guards(close,close,self.equity,self.bp)
                if not ok:continue
                due=entry_minute(t-2)
                phase_end=690 if t<690 else 925
                a={'date':self.day,'bp':self.bp,'symbol':symbol,'decision':t,
                   'entry_due':due,'planned_close':close,'planned_quantity':100,'stop':stop,'target':target,
                   'equity_at_selection':str(self.equity),'planned_loss':str(loss),
                   'planned_reward':str(reward),'exit_due':min(due+10,phase_end-1),
                   'exit_cause':'time10','outcome':'PENDING','released':None}
                self.intents.append(a);self.active=a;selected=symbol;reason='SELECTED';break
        self.reason(reason)
        self.emit({'date':self.day,'minute':t,'bp':self.bp,'reason':reason,
                   'candidate_count':len(candidates),'selected':selected})

    def process_day(self,date,symbols,signals):
        self.begin(date)
        if self.stopped:
            self.days.append({'date':date,'status':'NOT_EVALUATED_AFTER_STOP','net':None});return
        for start,end in SESSIONS:
            for t in range(start,end+1):
                self.settle(t,symbols)
                if self.stopped:break
                if t<end:self.open(t,symbols)
                self.decide(t,symbols,signals)
            if self.stopped:break
            if self.active is not None:
                self.unknown(end,'SESSION_BOUNDARY_UNRESOLVED');break
        self.days.append({'date':date,'status':'UNRESOLVED' if self.stopped else 'COMPLETE',
                          'net':None if self.stopped else str(self.daily_net),
                          'partial_settled_net':str(self.daily_net),
                          'opening_equity':str(self.day_open),
                          'last_settled_equity':str(self.equity)})

    def result(self):
        closed=[a for a in self.intents if a['outcome']=='CLOSED']
        profit=sum((max(F(a['net']),0) for a in closed),F())
        loss=sum((max(-F(a['net']),0) for a in closed),F())
        cash=peak=F(400000);dd=F();byday={};bysym={}
        for a in closed:
            n=F(a['net']);cash+=n;peak=max(peak,cash);dd=max(dd,peak-cash)
            byday[a['date']]=byday.get(a['date'],F())+n
            bysym[a['symbol']]=bysym.get(a['symbol'],F())+n
        total=cash-400000
        complete=not self.stopped and len(self.days)==20 and all(d['status']=='COMPLETE' for d in self.days)
        return {'bp':self.bp,'status':'COMPLETE20' if complete else 'UNRESOLVED_PARTIAL',
            'complete_dates':sum(d['status']=='COMPLETE' for d in self.days),
            'selected_intents':len(self.intents),'completed_trades':len(closed),
            'unresolved':sum(a['outcome']=='UNRESOLVED' for a in self.intents),
            'declined_actual_guard':sum(a['outcome']=='DECLINED_ACTUAL_GUARD' for a in self.intents),
            'period_net':str(total) if complete else None,'partial_realized_net':str(total),
            'last_settled_equity':str(cash),'gross':str(sum((F(a['gross']) for a in closed),F())),
            'friction':str(sum((F(a['friction']) for a in closed),F())),
            'finance':str(sum((F(a['finance']) for a in closed),F())),
            'PF':str(profit/loss) if loss else None,'realized_equity_DD':str(dd),
            'max_completed_trade_loss':str(max([F()]+[-F(a['net']) for a in closed])),
            'trade_dates':len(byday),'net_without_best_day':str(total-max(byday.values(),default=F())),
            'net_without_best_symbol':str(total-max(bysym.values(),default=F())),
            'days':self.days,'reason_counts':self.reason_counts,'intents':self.intents,
            'partial_never_whole_period':not complete,'tick_or_mark_to_market_DD_verified':False}
