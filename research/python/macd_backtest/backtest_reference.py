"""Independent integer EMA, local trade replay, selection and cash audit."""
from fractions import Fraction as Q


def check(x,msg):
    if not x:raise AssertionError(msg)


def integer_ema(xs,n):
    out=[None]*len(xs)
    if len(xs)<n:return out
    numerator=sum(xs[:n]);denominator=n
    out[n-1]=Q(numerator,denominator)
    for i in range(n,len(xs)):
        numerator=(n-1)*numerator+2*xs[i]*denominator
        denominator*=n+1
        out[i]=Q(numerator,denominator)
    return out


def ref_macd(xs):
    f,s=integer_ema(xs,12),integer_ema(xs,26);out=[None]*len(xs)
    ms=[f[i]-s[i] for i in range(25,len(xs))]
    if len(ms)<9:return out
    sig=sum(ms[:9],Q())/9
    out[33]=(ms[8],sig,ms[8]-sig)
    for i in range(9,len(ms)):
        sig+=(ms[i]-sig)/5
        out[i+25]=(ms[i],sig,ms[i]-sig)
    return out


def ref_guard(close,entry,equity,bp):
    if equity<=0:return False
    s=close*995//1000;t=close*1015//1000;n=Q(entry,100)
    finance=-(-(n*Q(28,365000)).numerator//(n*Q(28,365000)).denominator)
    c=n*Q(bp,10000)+finance
    return (0<s<entry<t and 0<Q(entry-s,100)+c<=equity/200
            and Q(t-entry,100)-c>=2*(Q(entry-s,100)+c)
            and n<=1320000 and n<=equity*Q(33,10)
            and max(Q(300000),n*Q(3,10))+finance+n/500<=equity)


def verify_features(date,symbols,primary):
    audited=0
    for symbol,rec in sorted(symbols.items()):
        expected={}
        if not rec['corporate']:
            for lo,hi in ((540,690),(750,925)):
                chunks=[];run=[]
                for m in range(lo,hi-1,2):
                    a,b=rec['bars'].get(m),rec['bars'].get(m+1)
                    if a is None or b is None or not a.good or not b.good:
                        if run:chunks.append(run);run=[]
                    else:run.append((m,b.c))
                if run:chunks.append(run)
                for chunk in chunks:
                    vals=ref_macd([c for m,c in chunk])
                    for i in range(34,len(chunk)):
                        p,c=vals[i-1][2],vals[i][2]
                        side=1 if p<=0<c else -1 if p>=0>c else 0
                        expected[chunk[i][0]+2]=(side,chunk[i][1],vals[i],i+1)
        got=primary[symbol]
        check(set(expected)==set(got),'REFERENCE_FEATURE_KEYS')
        for t,(side,close,indicators,n) in expected.items():
            check((got[t]['side'],got[t]['close'],(got[t]['macd'],got[t]['signal'],got[t]['histogram']),got[t]['run_bars'])==(side,close,indicators,n),'REFERENCE_MACD_VALUE')
            audited+=1
    return audited


def local_replay(intent,bars,signals,bp):
    """Reconstruct only this intent's path, not using engine settlement helpers."""
    e=intent['entry_due'];close=intent['planned_close'];eq=Q(intent['equity_at_selection'])
    s=close*995//1000;t=close*1015//1000;b=bars.get(e)
    if b is None or not b.good or b.o<=0:
        return {'outcome':'UNRESOLVED','recognized':e+1,'released':None}
    if not ref_guard(close,b.o,eq,bp):
        return {'outcome':'DECLINED_ACTUAL_GUARD','recognized':e+1,'released':e+1}
    entry=b.o;due=e+10;cause='time10';end=690 if e<690 else 925
    for m in range(e,end):
        # At m-1 a closed2m down-cross may schedule this minute's Open.
        if m>e and signals.get(m-1,{}).get(intent['symbol'],{}).get('side')==-1 and m<due:
            due=m;cause='macd'
        b=bars.get(m)
        if b is None or not b.good:return {'outcome':'UNRESOLVED','recognized':m+1,'released':None}
        price=None;reason=None
        if b.o<=s:price,reason=b.o,'adverse_open'
        elif b.o>=t:price,reason=t,'target_cap'
        elif m>=due:price,reason=b.o,cause+'_open'
        elif b.l<=s:price,reason=s,'stop_first'
        elif b.h>=t:price,reason=t,'target'
        needed=(100 if m==e else 0)+(100 if price is not None else 0)
        if b.v<needed:return {'outcome':'UNRESOLVED','recognized':m+1,'released':None}
        if price is not None:
            notional=Q(entry,100);finance_q=notional*Q(28,365000)
            finance=-(-finance_q.numerator//finance_q.denominator)
            friction=notional*Q(bp,10000);gross=Q(price-entry,100)
            return {'outcome':'CLOSED','entry_price':entry,'exit_bar':m,'exit_price':price,
                'recognized':m+1,'released':m+1,'reason':reason,'gross':str(gross),
                'friction':str(friction),'finance':str(finance),'net':str(gross-friction-finance)}
    raise AssertionError('REFERENCE_DID_NOT_TERMINATE')


def verify_book(book_result,all_days,all_signals):
    intents=book_result['intents'];bp=book_result['bp'];cash=Q(400000);checked=0
    for a in intents:
        check(a['entry_due']==a['decision']+1,'ENTRY_AFTER_SEAL_DELAY')
        check(ref_guard(a['planned_close'],a['planned_close'],Q(a['equity_at_selection']),bp),'PLANNED_GUARD')
        expected=local_replay(a,all_days[a['date']][a['symbol']]['bars'],all_signals[a['date']],bp)
        for k,v in expected.items():check(a.get(k)==v,'LOCAL_PATH_'+k)
        if a['outcome']=='CLOSED':
            check(Q(a['equity_before_settlement'])==cash,'CASH_BEFORE')
            cash+=Q(expected['net']);check(Q(a['equity_after_settlement'])==cash,'CASH_AFTER')
        checked+=1
    check(cash==Q(book_result['last_settled_equity']),'FINAL_PARTIAL_CASH')
    # Detect omitted eligible entries, wrong priority and premature slot release.
    cash=Q(400000);day_open=cash;active=None;halt=False;bydate={}
    for a in intents:bydate.setdefault(a['date'],{})[a['decision']]=a
    decision_audits=0
    for date,symbols in sorted(all_days.items()):
        opening=cash;daily=Q();selected=bydate.get(date,{})
        for t in range(540,926):
            if active is not None and active['date']==date:
                if active['outcome']=='UNRESOLVED' and active['recognized']<=t:halt=True
                if active.get('released') is not None and active['released']<=t:
                    if active['outcome']=='CLOSED':cash+=Q(active['net']);daily+=Q(active['net'])
                    active=None
            if halt:continue
            candidates=[]
            for symbol,v in sorted(all_signals[date].get(t,{}).items()):
                start=t-2
                end=690 if 540<=start<690 else 925 if 750<=start<925 else 0
                if v['side']==1 and t+11<end:candidates.append((symbol,v['close']))
            if not candidates:continue
            decision_audits+=1
            first=None
            if active is None and daily>-opening/100:
                for s,c in candidates:
                    if ref_guard(c,c,cash,bp):first=s;break
            got=selected.get(t)
            check((got['symbol'] if got else None)==first,'INDEPENDENT_SELECTION_PRIORITY')
            if got:
                check(Q(got['equity_at_selection'])==cash,'SELECTION_CASH');active=got
        check(halt or active is None,'NO_LUNCH_OR_OVERNIGHT_POSITION')
    closed=[x for x in intents if x['outcome']=='CLOSED']
    for field in ('gross','friction','finance'):
        check(Q(book_result[field])==sum((Q(a[field]) for a in closed),Q()),'TOTAL_'+field)
    net=sum((Q(a['net']) for a in closed),Q())
    check(Q(book_result['partial_realized_net'])==net,'TOTAL_NET')
    full_expected=not halt and len(all_days)==20
    check(book_result['period_net'] is None if not full_expected else Q(book_result['period_net'])==net,'WHOLE_VS_PARTIAL')
    return {'intents_independently_replayed':checked,'signal_batches_independently_selected':decision_audits,
            'partial_cash_exact':True,'missing_or_extra_entry_check':True}
