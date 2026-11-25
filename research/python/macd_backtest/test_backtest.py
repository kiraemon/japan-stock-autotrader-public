import unittest
from fractions import Fraction as F
from .model import Bar, macd
from .backtest_engine import Book,fast_features
from .backtest_reference import ref_macd,ref_guard,local_replay,verify_book,verify_features

D='2025-04-01'


def bar(m,o=10000000,h=None,l=None,c=None,v=1000,good=True):
    h=o if h is None else h;l=o if l is None else l;c=o if c is None else c
    return Bar(m,o,h,l,c,v,o*v,good)


def day(symbols=('1000@1',)):
    return {s:{'corporate':False,'bars':{m:bar(m) for lo,hi in ((540,690),(750,925)) for m in range(lo,hi)}} for s in symbols}


def signal(t=610,s='1000@1',side=1):return {t:{s:{'side':side,'close':10000000}}}


class AccountTests(unittest.TestCase):
    def execute(self,rows,sigs,bp=10):
        b=Book(bp);b.process_day(D,rows,sigs);return b

    def test_macd_integer_reference_full_nonconstant(self):
        xs=[10000000+i*i-(i%7)*500 for i in range(90)]
        self.assertEqual(macd(xs),ref_macd(xs))

    def test_real_shape_feature_independent_audit(self):
        rows=day();values,sigs,counts=fast_features(D,rows)
        self.assertEqual(verify_features(D,rows,values),94)
        self.assertEqual(counts['scheduled'],82)

    def test_time_exit_delay_and_cash_seal(self):
        b=self.execute(day(),signal());a=b.intents[0]
        self.assertEqual((a['entry_due'],a['exit_bar'],a['recognized']),(611,621,622))
        self.assertEqual(a['net'],'-108')

    def test_downcross_exit_has_same_delay(self):
        sig=signal();sig.update(signal(614,side=-1))
        a=self.execute(day(),sig).intents[0]
        self.assertEqual((a['exit_bar'],a['recognized'],a['reason']),(615,616,'macd_open'))

    def test_samebar_entry_exit_needs200_at_seal(self):
        rows=day();rows['1000@1']['bars'][611]=bar(611,h=10200000,l=9900000,v=199)
        b=self.execute(rows,signal());a=b.intents[0]
        self.assertEqual(a['outcome'],'UNRESOLVED');self.assertEqual(a['recognized'],612)
        self.assertEqual(b.equity,F(400000))

    def test_samebar_stop_first(self):
        rows=day();rows['1000@1']['bars'][611]=bar(611,h=10200000,l=9900000,v=200)
        a=self.execute(rows,signal()).intents[0]
        self.assertEqual((a['outcome'],a['reason'],a['net']),('CLOSED','stop_first','-608'))

    def test_missing_entry_stops_at_seal_no_replacement(self):
        rows=day(('1000@1','2000@1'));del rows['1000@1']['bars'][611]
        sig=signal();sig[610].update(signal(s='2000@1')[610])
        b=self.execute(rows,sig)
        self.assertEqual(len(b.intents),1);self.assertEqual(b.intents[0]['symbol'],'1000@1')
        self.assertEqual(b.intents[0]['recognized'],612);self.assertTrue(b.stopped)

    def test_held_missing_keeps_obligation(self):
        rows=day();del rows['1000@1']['bars'][615]
        b=self.execute(rows,signal());self.assertEqual(b.intents[0]['outcome'],'UNRESOLVED')
        self.assertIsNone(b.intents[0]['released']);self.assertEqual(b.equity,F(400000))

    def test_held_low_volume_does_not_require100_without_fill(self):
        rows=day();rows['1000@1']['bars'][614]=bar(614,v=1)
        self.assertEqual(self.execute(rows,signal()).intents[0]['outcome'],'CLOSED')

    def test_actual_guard_rejection_reserves_until_seal(self):
        rows=day();rows['1000@1']['bars'][611]=bar(611,o=10100000)
        b=Book(10);b.begin(D);b.decide(610,rows,signal());b.open(611,rows)
        self.assertIsNotNone(b.active);b.settle(612,rows)
        self.assertIsNone(b.active);self.assertEqual(b.intents[0]['outcome'],'DECLINED_ACTUAL_GUARD')

    def test_bad_future_bar_does_not_change_selection(self):
        rows=day(('1000@1','2000@1'));rows['1000@1']['bars'][611]=bar(611,good=False)
        sig=signal();sig[610].update(signal(s='2000@1')[610])
        a=self.execute(rows,sig).intents[0]
        self.assertEqual(a['symbol'],'1000@1');self.assertEqual(a['outcome'],'UNRESOLVED')

    def test_release_allows_new_selection_only_after_seal(self):
        rows=day(('1000@1','2000@1'));sig=signal();sig.update(signal(620,'2000@1'));sig.update(signal(622,'2000@1'))
        b=self.execute(rows,sig);self.assertEqual([x['decision'] for x in b.intents],[610,622])

    def test_lunch_and_auction_new_entry_refused(self):
        rows=day();sig=signal(690);sig.update(signal(924))
        self.assertEqual(self.execute(rows,sig).intents,[])

    def test_last_am_position_closed_before_lunch(self):
        a=self.execute(day(),signal(678)).intents[0]
        self.assertEqual((a['entry_due'],a['exit_bar'],a['recognized']),(679,689,690))

    def test_unresolved_future_dates_not_zero_returns(self):
        rows=day();del rows['1000@1']['bars'][611];b=self.execute(rows,signal())
        b.process_day('2025-04-02',rows,{})
        self.assertIsNone(b.days[1]['net']);self.assertEqual(b.days[1]['status'],'NOT_EVALUATED_AFTER_STOP')

    def test_independent_local_path_and_selection(self):
        rows=day(('1000@1','2000@1'));sig=signal();sig[610].update(signal(s='2000@1')[610])
        b=self.execute(rows,sig);r=b.result()
        proof=verify_book(r,{D:rows},{D:sig});self.assertTrue(proof['missing_or_extra_entry_check'])

    def test_independent_unknown_path(self):
        rows=day();del rows['1000@1']['bars'][615];sig=signal();b=self.execute(rows,sig)
        self.assertEqual(local_replay(b.intents[0],rows['1000@1']['bars'],sig,10)['recognized'],616)
        verify_book(b.result(),{D:rows},{D:sig})

    def test_independent_guard_boundary_values(self):
        from .model import guards
        for e in range(9900000,10150000,25000):
            for bp in (10,20):self.assertEqual(guards(10000000,e,F(400000),bp)[0],ref_guard(10000000,e,F(400000),bp))

    def test_daily_loss_stop_and_separate_fee_books(self):
        rows=day();sig={}
        for t in range(610,626,2):
            sig.update(signal(t));rows['1000@1']['bars'][t+1]=bar(t+1,l=9900000)
        a=self.execute(rows,sig,10);b=self.execute(rows,sig,20)
        self.assertEqual(len(a.intents),7)
        # 20bp + finance makes fixed0.5%/1.5% brackets fail net2R by construction.
        self.assertEqual(len(b.intents),0)
        self.assertGreater(a.reason_counts['DAILY_LOSS_STOP'],0)
        verify_book(a.result(),{D:rows},{D:sig})
        verify_book(b.result(),{D:rows},{D:sig})

    def test_stop_gap_can_exceed_entry_risk_budget(self):
        rows=day();rows['1000@1']['bars'][612]=bar(612,o=9000000)
        a=self.execute(rows,signal()).intents[0]
        self.assertEqual((a['reason'],a['net']),('adverse_open','-10108'))


if __name__=='__main__':unittest.main()
