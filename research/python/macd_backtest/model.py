"""Pure preparation functions. Prices and turnover use the inherited 1e4 scale.

No acquisition, orders, PnL engine, live clock, or RSI is implemented here.
"""
from dataclasses import dataclass
from fractions import Fraction as F

KNOWN_DATES = tuple("2025-04-" + d for d in (
    "01", "02", "03", "04", "07", "08", "09", "10", "11", "14",
    "15", "16", "17", "18", "21", "22", "23", "24", "25", "28"))
SESSIONS = ((540, 690), (750, 925))


class Blocked(ValueError):
    pass


def require(value, reason):
    if not value:
        raise Blocked(reason)


def guard_date(date):
    require(date in KNOWN_DATES, "OUTSIDE_KNOWN20_NO_PRICE_ACCESS")


@dataclass(frozen=True)
class Bar:
    minute: int
    o: int
    h: int
    l: int
    c: int
    v: int
    va: int
    good: bool = True

    def validate(self):
        require(all(type(x) is int for x in (
            self.minute, self.o, self.h, self.l, self.c, self.v, self.va)), "BAR_INTEGER")
        require(type(self.good) is bool, "BAR_GOOD_TYPE")
        if self.good:
            require(0 < self.l <= min(self.o, self.c) <= max(self.o, self.c) <= self.h,
                    "OHLC_ENVELOPE")
            require(self.v > 0 and self.l*self.v <= self.va <= self.h*self.v,
                    "VOLUME_TURNOVER")


def aggregate(date, rows, corporate=False):
    guard_date(date)  # Must precede even iteration over price-bearing objects.
    require(type(corporate) is bool, "CORPORATE_TYPE")
    by = {}
    for b in rows:
        b.validate()
        require(b.minute not in by, "DUPLICATE_MINUTE")
        by[b.minute] = b
    if corporate:
        return {}
    out = {}
    for start, end in SESSIONS:
        for m in range(start, end-1, 2):
            a, b = by.get(m), by.get(m+1)
            if a is None or b is None or not a.good or not b.good:
                continue
            out[m] = Bar(m, a.o, max(a.h, b.h), min(a.l, b.l), b.c,
                         a.v+b.v, a.va+b.va)
    return out


def ema(values, n):
    require(n > 0, "EMA_PERIOD")
    out = [None]*len(values)
    if len(values) < n:
        return out
    out[n-1] = sum(map(F, values[:n]), F())/n
    for i in range(n, len(values)):
        out[i] = (out[i-1]*(n-1) + F(values[i])*2)/(n+1)
    return out


def macd(closes):
    fast, slow = ema(closes, 12), ema(closes, 26)
    out = [None]*len(closes)
    ms = [fast[i]-slow[i] for i in range(25, len(closes))]
    sig = ema(ms, 9)
    for i in range(33, len(closes)):
        out[i] = (ms[i-25], sig[i-25], ms[i-25]-sig[i-25])
    return out


def features(bars):
    """Reset at each session and missing two-minute slot; never bridge lunch."""
    out = {}
    for start, end in SESSIONS:
        run = []
        for m in range(start, end-1, 2):
            b = bars.get(m)
            if b is None:
                run = []
                continue
            run.append(b.c)
            value = macd(run)[-1]
            if value is not None:
                out[m] = (value, len(run))
    return out


def cross(previous, current):
    if previous is None or current is None:
        return 0
    a, b = previous[2], current[2]
    return 1 if a <= 0 < b else -1 if a >= 0 > b else 0


def entry_minute(two_minute_start):
    # Label t contains [t,t+2); a full extra minute follows the modeled seal.
    for start, end in SESSIONS:
        if start <= two_minute_start and two_minute_start+2 <= end:
            require((two_minute_start-start) % 2 == 0, "PAIR_ALIGNMENT")
            entry = two_minute_start+3
            return entry if entry+10 < end else None
    return None


def ceil(x):
    x = F(x)
    return -(-x.numerator//x.denominator)


def fees(entry_scaled, bp=10, qty=100):
    require(bp in (10, 20) and qty == 100 and entry_scaled > 0, "COST_SCOPE")
    notional = F(entry_scaled*qty, 10000)
    finance = ceil(notional*F(280, 10000*365))
    return notional*F(bp, 10000)+finance


def guards(planned_close, actual_open, equity=F(400000), bp=10):
    """One fixed100-share long; research price grid, not dated exchange ticks."""
    require(planned_close > 0 and actual_open > 0 and equity > 0, "RISK_INPUT")
    stop, target = planned_close*995//1000, planned_close*1015//1000
    n = F(actual_open*100, 10000)
    cost = fees(actual_open, bp)
    loss, reward = F((actual_open-stop)*100, 10000)+cost, F((target-actual_open)*100, 10000)-cost
    reserve = max(F(300000), n*F(3, 10))+ceil(n*F(280, 10000*365))+n*F(20, 10000)
    ok = (0 < stop < actual_open < target and 0 < loss <= equity/200 and reward >= 2*loss
          and n <= min(F(1320000), equity*F(33, 10)) and reserve <= equity)
    return ok, stop, target, loss, reward


def exit_reference(bar, stop, target, timed=False):
    bar.validate()
    require(bar.good, "UNKNOWN_EXIT_BAR")
    if bar.o <= stop:
        return bar.o, "adverse_open"
    if bar.o >= target:
        return target, "target_cap"
    if timed:
        return bar.o, "time_open"
    if bar.l <= stop:  # Both touched in one minute: adverse ordering.
        return stop, "stop_first"
    if bar.h >= target:
        return target, "target"
    return None, "held"


def capacity_at_seal(volume, entry=0, exit=0):
    require(entry in (0, 100) and exit in (0, 100), "FIXED_QUANTITY")
    return volume >= entry+exit
