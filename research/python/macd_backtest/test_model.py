import unittest
from fractions import Fraction as F
from .model import (Bar, Blocked, aggregate, ema, macd, features, cross,
                    entry_minute, fees, guards, exit_reference, capacity_at_seal)


def bar(m, price=10000000):
    return Bar(m, price, price, price, price, 100, price*100)


class PreparationTests(unittest.TestCase):
    def test_pair_exact_ohlcv_value(self):
        a=Bar(540,100,110,95,105,10,1020)
        b=Bar(541,105,115,100,112,20,2150)
        self.assertEqual(aggregate('2025-04-01',[a,b])[540],
                         Bar(540,100,115,95,112,30,3170))

    def test_no_lunch_bridge_or_auction_tail(self):
        out=aggregate('2025-04-01',[bar(689),bar(750),bar(924),bar(925)])
        self.assertEqual(out,{})

    def test_missing_and_bad_pair_not_filled(self):
        bad=Bar(541,0,0,0,0,0,0,False)
        self.assertEqual(aggregate('2025-04-01',[bar(540),bad]),{})

    def test_duplicate_rejected(self):
        with self.assertRaises(Blocked):aggregate('2025-04-01',[bar(540),bar(540)])

    def test_corporate_mask_retained_as_exclusion(self):
        self.assertEqual(aggregate('2025-04-01',[bar(540),bar(541)],True),{})

    def test_protected_scope_before_price_iteration(self):
        class Poison:
            def __iter__(self):raise AssertionError('prices accessed')
        for d in ('2025-04-30','2025-05-01','2026-11-04'):
            with self.subTest(date=d):
                with self.assertRaises(Blocked):aggregate(d,Poison())

    def test_bad_value_units_rejected(self):
        with self.assertRaises(Blocked):aggregate('2025-04-01',[Bar(540,10,10,10,10,100,1)])

    def test_ema_independent_weighted_formula(self):
        xs=[i*i+3*i+7 for i in range(80)]
        for n in (9,12,26):
            result=ema(xs,n);q=F(n-1,n+1);a=F(2,n+1);seed=F(sum(xs[:n]),n)
            for i in range(n-1,len(xs)):
                expected=seed*q**(i-n+1)+sum((a*xs[k]*q**(i-k) for k in range(n,i+1)),F())
                self.assertEqual(result[i],expected)

    def test_signal_seed_and_constant_prices(self):
        out=macd([100]*40)
        self.assertTrue(all(v is None for v in out[:33]))
        self.assertEqual(out[33],(F(),F(),F()))

    def test_future_prices_do_not_change_earlier_values(self):
        a=list(range(100));b=a[:60]+[1000000]*40
        self.assertEqual(macd(a)[:60],macd(b)[:60])

    def test_gap_and_session_reset(self):
        bs={m:bar(m) for m in range(540,690,2)}
        bs.update({m:bar(m) for m in range(750,925,2)})
        del bs[620]
        f=features(bs)
        self.assertNotIn(622,f)
        self.assertNotIn(750,f)
        self.assertIn(816,f)  # PM first34 bars.

    def test_cross_strict_equality(self):
        point=lambda h:(F(),F(),F(h))
        self.assertEqual(cross(point(0),point(1)),1)
        self.assertEqual(cross(point(0),point(-1)),-1)
        self.assertEqual(cross(point(0),point(0)),0)
        self.assertEqual(cross(None,point(1)),0)

    def test_entry_is_after_seal_and_processing_delay(self):
        self.assertEqual(entry_minute(608),611)
        self.assertEqual(entry_minute(676),679)
        self.assertIsNone(entry_minute(678))
        self.assertEqual(entry_minute(910),913)
        self.assertIsNone(entry_minute(912))
        self.assertIsNone(entry_minute(924))

    def test_finance_and_friction_once(self):
        self.assertEqual(fees(10000000),F(108))
        self.assertEqual(fees(10000000,20),F(208))

    def test_actual_open_rechecks_fixed_bracket(self):
        self.assertTrue(guards(10000000,10000000)[0])
        self.assertFalse(guards(10000000,10100000)[0])
        self.assertFalse(guards(10000000,10000000,F(299999))[0])

    def test_intrabar_ambiguity_uses_stop_first(self):
        b=Bar(600,100,120,80,100,200,20000)
        self.assertEqual(exit_reference(b,90,110),(90,'stop_first'))
        self.assertEqual(exit_reference(b,90,110,True),(100,'time_open'))

    def test_gap_and_target_cap(self):
        self.assertEqual(exit_reference(Bar(600,80,90,70,80,100,8000),90,110),(80,'adverse_open'))
        self.assertEqual(exit_reference(Bar(600,120,130,115,120,100,12000),90,110),(110,'target_cap'))

    def test_capacity_is_post_seal_obligation(self):
        self.assertFalse(capacity_at_seal(199,100,100))
        self.assertTrue(capacity_at_seal(200,100,100))
        self.assertTrue(capacity_at_seal(0))


if __name__=='__main__':unittest.main()
