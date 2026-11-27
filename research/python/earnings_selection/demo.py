"""Run the comparison on generated inputs from the repository root."""

from datetime import date, datetime, time, timedelta
import json

from .study import EarningsSelection, JST, MarketCandidate, compare_day

DECISION = date(2030, 1, 7)
HOLIDAY = date(2030, 1, 14)


def stamp(hour: int, minute: int = 0) -> datetime:
    return datetime.combine(DECISION, time(hour, minute), JST)


def inputs() -> tuple:
    days = [DECISION + timedelta(days=offset) for offset in range(20)]
    sessions = [day for day in days if day.weekday() < 5 and day != HOLIDAY]
    market = [
        MarketCandidate("SYN-A", stamp(16), 105_000, 80_000_000),
        MarketCandidate("SYN-B", stamp(16), 95_000, 100_000_000),
    ]
    selections = [
        EarningsSelection("SYN-A", stamp(15), stamp(16), stamp(17), "Generated earnings note")
    ]
    # Exact calendar sessions for the generated example, including the closure.
    entry, exit_day = date(2030, 1, 8), date(2030, 1, 23)
    opens = {
        (entry, "SYN-A"): 100_001,
        (exit_day, "SYN-A"): 105_001,
        (entry, "SYN-B"): 90_000,
        (exit_day, "SYN-B"): 91_000,
    }
    return sessions, market, selections, opens


def main() -> None:
    print(json.dumps(compare_day(DECISION, *inputs()), indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
