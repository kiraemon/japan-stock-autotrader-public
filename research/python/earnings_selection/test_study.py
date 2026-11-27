from dataclasses import replace
from datetime import date
import unittest

from .demo import DECISION, inputs, stamp
from .study import EarningsSelection, MarketCandidate, compare_day


class ComparisonTests(unittest.TestCase):
    def test_exact_sessions_after_closure_and_fee_rounding(self):
        result = compare_day(DECISION, *inputs())
        self.assertEqual((result["entry_day"], result["exit_day"]), ("2030-01-08", "2030-01-23"))
        self.assertEqual((result["slots_per_group"], result["shares"], result["round_trip_bp"]), (1, 100, 10))
        earnings, control = result["earnings"], result["control"]
        self.assertEqual((earnings["symbol"], control["symbol"]), ("SYN-A", "SYN-B"))
        self.assertEqual((earnings["gross_sen"], earnings["cost_sen"], earnings["net_sen"]), (500_000, 10_001, 489_999))
        self.assertEqual((control["gross_sen"], control["cost_sen"], control["net_sen"]), (100_000, 9_000, 91_000))
        self.assertEqual(result["net_difference_sen"], 398_999)

    def test_cutoff_for_disclosure_availability_and_human_confirmation(self):
        sessions, market, _, opens = inputs()
        for hours, expected in [
            ((19, 19, 19), "disclosed_after_cutoff"),
            ((17, 19, 19), "available_after_cutoff"),
            ((17, 18, 19), "confirmed_after_cutoff"),
            ((17, 18, 18), "eligible"),
        ]:
            with self.subTest(expected=expected):
                selection = EarningsSelection("SYN-A", *(stamp(h) for h in hours), "Generated")
                result = compare_day(DECISION, sessions, market, [selection], opens)
                audit = result["selection_audit"][0]
                self.assertEqual(audit["status"], expected)
                self.assertEqual(audit["available_at"], stamp(hours[1]).isoformat())
                self.assertEqual(result["earnings"]["planned"], int(expected == "eligible"))
                self.assertEqual(result["control"]["symbol"], "SYN-B")

    def test_both_groups_apply_the_same_market_eligibility(self):
        sessions, market, selections, opens = inputs()
        for row in [
            MarketCandidate("SYN-C", stamp(19), 100_000, 200_000_000),
            MarketCandidate("SYN-C", stamp(17), 500_001, 200_000_000),
            MarketCandidate("SYN-C", stamp(17), 100_000, 49_999_999),
        ]:
            with self.subTest(row=row):
                selection = replace(selections[0], symbol="SYN-C", confirmed_at=stamp(16, 30))
                result = compare_day(DECISION, sessions, [row, *market], [selection, *selections], opens)
                audit = {entry["symbol"]: entry["status"] for entry in result["selection_audit"]}
                self.assertEqual(audit["SYN-C"], "outside_common_universe")
                self.assertEqual(result["common_symbols"], ["SYN-A", "SYN-B"])
                self.assertEqual((result["earnings"]["symbol"], result["control"]["symbol"]), ("SYN-A", "SYN-B"))

    def test_missing_exact_open_retains_candidate_and_unknown(self):
        sessions, market, selections, opens = inputs()
        selections.append(replace(selections[0], symbol="SYN-B", confirmed_at=stamp(17, 30)))
        for missing_date, later_date, expected in [
            (date(2030, 1, 8), date(2030, 1, 9), "unknown_entry"),
            (date(2030, 1, 23), date(2030, 1, 24), "unknown_exit"),
        ]:
            with self.subTest(expected=expected):
                prices = dict(opens)
                del prices[(missing_date, "SYN-A")]
                prices[(later_date, "SYN-A")] = 130_000
                result = compare_day(DECISION, sessions, market, selections, prices)
                earnings = result["earnings"]
                self.assertEqual((earnings["symbol"], earnings["status"]), ("SYN-A", expected))
                self.assertEqual((earnings["planned"], earnings["evaluated"], earnings["unknown"]), (1, 0, 1))
                self.assertIsNone(earnings["net_sen"])
                self.assertEqual(result["control"]["status"], "evaluated")
                self.assertIsNone(result["net_difference_sen"])

    def test_shared_candidate_and_input_order_are_reproducible(self):
        sessions, market, selections, opens = inputs()
        market[0] = replace(market[0], average_turnover_yen=market[1].average_turnover_yen)
        selections.append(replace(selections[0], symbol="SYN-B"))
        first = compare_day(DECISION, sessions, market, selections, opens)
        second = compare_day(DECISION, sessions, market[::-1], selections[::-1], opens)
        self.assertEqual(first, second)
        self.assertEqual(first["shared_symbol"], "SYN-A")
        self.assertEqual(first["earnings"], first["control"])
        self.assertEqual(first["net_difference_sen"], 0)

    def test_no_candidate_and_late_snapshots_are_visible_in_counts(self):
        sessions, market, selections, opens = inputs()
        late_market = [replace(row, observed_at=stamp(19)) for row in market]
        result = compare_day(DECISION, sessions, late_market, selections, opens)
        for name in ("earnings", "control"):
            group = result[name]
            self.assertEqual((group["planned"], group["evaluated"], group["unknown"]), (0, 0, 0))
            self.assertEqual(group["status"], "no_candidate")
            self.assertIsNone(group["net_sen"])

    def test_reject_ambiguous_calendar_and_duplicate_candidate_records(self):
        sessions, market, selections, opens = inputs()
        with self.assertRaisesRegex(ValueError, "scheduled exit"):
            compare_day(DECISION, sessions[:11], market, selections, opens)
        with self.assertRaisesRegex(ValueError, "ordered, unique"):
            compare_day(DECISION, [sessions[0], *sessions], market, selections, opens)
        with self.assertRaisesRegex(ValueError, "duplicate symbol"):
            compare_day(DECISION, sessions, market, selections * 2, opens)


if __name__ == "__main__":
    unittest.main()
