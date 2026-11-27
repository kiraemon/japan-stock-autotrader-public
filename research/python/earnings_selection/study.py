"""Compare one decision date using an explicit trading calendar and prices in sen."""

from dataclasses import dataclass
from datetime import date, datetime, time, timedelta, timezone
from typing import Mapping, Sequence

JST = timezone(timedelta(hours=9))
CUTOFF = time(18, 0)
HOLD_SESSIONS = 10
SHARES = 100
ROUND_TRIP_BP = 10
MIN_AVERAGE_TURNOVER_YEN = 50_000_000
MAX_LOT_COST_SEN = 50_000_000


@dataclass(frozen=True)
class MarketCandidate:
    symbol: str
    observed_at: datetime
    closing_price_sen: int
    average_turnover_yen: int


@dataclass(frozen=True)
class EarningsSelection:
    symbol: str
    disclosed_at: datetime
    available_at: datetime
    confirmed_at: datetime
    rationale: str


def _unique_by_symbol(records: Sequence) -> dict:
    by_symbol = {record.symbol: record for record in records}
    if len(by_symbol) != len(records):
        raise ValueError("duplicate symbol in decision input")
    return by_symbol


def _aware(*times: datetime) -> None:
    if any(stamp.utcoffset() is None for stamp in times):
        raise ValueError("decision timestamps must include a timezone")


def _selection_status(selection: EarningsSelection, cutoff: datetime, eligible: set[str]) -> str:
    disclosed, available, confirmed = selection.disclosed_at, selection.available_at, selection.confirmed_at
    _aware(disclosed, available, confirmed)
    if not disclosed <= available <= confirmed or not selection.rationale.strip():
        raise ValueError("selection requires an ordered source history and rationale")
    if disclosed > cutoff:
        return "disclosed_after_cutoff"
    if available > cutoff:
        return "available_after_cutoff"
    if confirmed > cutoff:
        return "confirmed_after_cutoff"
    if confirmed.astimezone(JST).date() != cutoff.date():
        return "different_decision_date"
    if selection.symbol not in eligible:
        return "outside_common_universe"
    return "eligible"


def _price(opens: Mapping[tuple[date, str], int | None], day: date, symbol: str) -> int | None:
    price = opens.get((day, symbol))
    if price is not None and (type(price) is not int or price <= 0):
        raise ValueError("opening price must be a positive integer in sen or None")
    return price


def _outcome(
    symbol: str | None, entry_day: date, exit_day: date, opens: Mapping[tuple[date, str], int | None]
) -> dict:
    status = "no_candidate"
    entry_price = exit_price = gross = cost = net = None
    if symbol is not None:
        entry_price = _price(opens, entry_day, symbol)
        if entry_price is None:
            status = "unknown_entry"
        else:
            exit_price = _price(opens, exit_day, symbol)
            if exit_price is None:
                status = "unknown_exit"
            else:
                status = "evaluated"
                gross = (exit_price - entry_price) * SHARES
                cost = (entry_price * SHARES * ROUND_TRIP_BP + 9_999) // 10_000
                net = gross - cost
    return {
        "symbol": symbol,
        "status": status,
        "planned": int(symbol is not None),
        "evaluated": int(status == "evaluated"),
        "unknown": int(status in {"unknown_entry", "unknown_exit"}),
        "entry_open_sen": entry_price,
        "exit_open_sen": exit_price,
        "gross_sen": gross,
        "cost_sen": cost,
        "net_sen": net,
    }


def compare_day(
    decision_day: date,
    sessions: Sequence[date],
    market: Sequence[MarketCandidate],
    selections: Sequence[EarningsSelection],
    opens: Mapping[tuple[date, str], int | None],
) -> dict:
    """Use decision-day snapshots and price exact dates after candidate selection."""
    if list(sessions) != sorted(set(sessions)) or decision_day not in sessions:
        raise ValueError("an ordered, unique trading calendar must contain the decision day")
    entry_index = sessions.index(decision_day) + 1
    exit_index = entry_index + HOLD_SESSIONS
    if exit_index >= len(sessions):
        raise ValueError("trading calendar must include the scheduled exit")
    entry_day, exit_day = sessions[entry_index], sessions[exit_index]
    cutoff = datetime.combine(decision_day, CUTOFF, JST)

    common = {}
    for symbol, row in _unique_by_symbol(market).items():
        _aware(row.observed_at)
        observed = row.observed_at.astimezone(JST)
        if observed.date() != decision_day or observed > cutoff:
            continue
        if (
            0 < row.closing_price_sen * SHARES <= MAX_LOT_COST_SEN
            and row.average_turnover_yen >= MIN_AVERAGE_TURNOVER_YEN
        ):
            common[symbol] = row

    audit = []
    eligible = []
    common_symbols = set(common)
    for symbol, selection in sorted(_unique_by_symbol(selections).items()):
        status = _selection_status(selection, cutoff, common_symbols)
        audit.append({
            "symbol": symbol,
            "status": status,
            "disclosed_at": selection.disclosed_at.astimezone(JST).isoformat(),
            "available_at": selection.available_at.astimezone(JST).isoformat(),
            "confirmed_at": selection.confirmed_at.astimezone(JST).isoformat(),
            "rationale": selection.rationale,
        })
        if status == "eligible":
            eligible.append(selection)

    earnings = min(eligible, key=lambda row: (row.confirmed_at, row.symbol), default=None)
    control = min(common.values(), key=lambda row: (-row.average_turnover_yen, row.symbol), default=None)
    earnings_result = _outcome(earnings.symbol if earnings else None, entry_day, exit_day, opens)
    control_result = _outcome(control.symbol if control else None, entry_day, exit_day, opens)
    earnings_net, control_net = earnings_result["net_sen"], control_result["net_sen"]
    return {
        "decision_cutoff": cutoff.isoformat(),
        "entry_day": entry_day.isoformat(),
        "exit_day": exit_day.isoformat(),
        "slots_per_group": 1,
        "shares": SHARES,
        "round_trip_bp": ROUND_TRIP_BP,
        "common_symbols": sorted(common),
        "selection_audit": audit,
        "earnings": earnings_result,
        "control": control_result,
        "shared_symbol": earnings.symbol if earnings and control and earnings.symbol == control.symbol else None,
        "net_difference_sen": earnings_net - control_net
        if earnings_net is not None and control_net is not None else None,
    }
