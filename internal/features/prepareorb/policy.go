package prepareorb

const PolicyVersion = "stocks-in-play-input-v0.2"

// JQuantsPrefixPolicy fixes the window at 09:00..09:04. It sums only observed
// trades, retaining absent leading minutes and the unverified source issues.
// It never invents bars, shifts the clock window, or skips reference dates.
const JQuantsPrefixPolicy = "jquants-fixed-window-leading-no-trades-v1"
