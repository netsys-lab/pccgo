package pccgo

import (
	"io"
	"math"
	"math/rand"

	"github.com/rs/zerolog"
	"go.uber.org/ratelimit"
)

type LoggingOptions struct {
	LogLevel zerolog.Level
	Target   io.Writer
}

type CongestionControlOptions struct {
	DisableCongestionControl bool
	FixRateLimitMbits        int
	StartRateMbits           int
	PayloadSize              int
	Logging                  *LoggingOptions
}

func NewCongestionControl(opts CongestionControlOptions) *CControlState {

	// TODO: Remove this probably
	if opts.StartRateMbits == 0 {
		opts.StartRateMbits = 1
	}

	mBytePerSecond := (opts.StartRateMbits * 1024 * 1024) / 8
	opsPerSecond := mBytePerSecond / opts.PayloadSize // TODO:

	if opts.DisableCongestionControl {
		opsPerSecond = math.MaxInt32
	}

	if opts.FixRateLimitMbits > 0 {
		mBytePerSecond = (opts.FixRateLimitMbits * 1024 * 1024) / 8
		opsPerSecond = mBytePerSecond / opts.PayloadSize
	}

	var logger *zerolog.Logger
	if opts.Logging != nil && opts.Logging.Target != nil {
		l := zerolog.New(opts.Logging.Target).With().Timestamp().Logger()
		logger = &l
		logger.Level(zerolog.InfoLevel)
	}

	logger.Debug().Int("Ops per second: ", opsPerSecond)

	return &CControlState{
		limiter:                  ratelimit.New(opsPerSecond),
		prevRate:                 float64(opsPerSecond),
		rCTs:                     make([]RCT, RCTSIntervals),
		rCTsIter:                 -1,
		rateBeforeRCTs:           1,
		eps:                      EpsMin,
		sign:                     1,
		state:                    PCCStartup,
		currRate:                 float64(opsPerSecond),
		prevUtility:              0,
		adjustIter:               1,
		maxRateLimit:             math.MaxFloat64, // float64(opsPerSecond),
		disableCongestionControl: opts.DisableCongestionControl,
		fixRateLimitMbits:        opts.FixRateLimitMbits,
		startRateMbits:           opts.StartRateMbits,
		payloadSize:              opts.PayloadSize,
		logger:                   logger,
		// MaxRateLimit:   100,
	}
}

func (cc *CControlState) UpdateRTT(rtt uint64) {
	cc.rTT = rtt

	m := float64(rand.Intn(6))/10.0 + 1.7 // m in [1.7, 2.2]
	cc.pccMIDuration = m * float64(cc.rTT)

	if cc.currRate == 0 {
		initialRate := uint32(MSS) / uint32(cc.rTT)
		cc.currRate = float64(initialRate)
		cc.prevRate = float64(initialRate)
	}

	// Restart current monitoring interval
	cc.startMonitoringInterval()
}

func (cc *CControlState) Limit() {
	if cc.disableCongestionControl {
		return
	}

	if cc.limiter == nil {
		return
	}

	cc.limiter.Take()
	cc.miTxPackets++
}

func (cc *CControlState) AddLoss(loss int) {
	if cc.disableCongestionControl {
		return
	}

	cc.miLoss += uint64(loss)
}
