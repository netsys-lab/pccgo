package pccgo

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/rs/zerolog"
	"go.uber.org/ratelimit"
)

// Assuming constants and types are defined elsewhere in the code
const (
	RCTSIntervals int     = 4 // Number of trials, ensure this is defined according to your setup
	EpsMin        float64 = 0.01
	EpsMax        float64 = 0.05
	MSS                   = 1460 // TODO: Support different MSS?
)
const (
	PCCUninitialized PCCState = iota
	PCCDecision
	PCCAdjust
	PCCStartup
	PCCTerminated
)

type RCTResult int

type PCCState int

const (
	Inconclusive RCTResult = iota
	Increase
	Decrease
)

type RCT struct {
	Rate    float64
	Utility float64
}

type CControlState struct {
	ticker                   *time.Ticker
	limiter                  ratelimit.Limiter
	prevRate                 float64
	rCTs                     []RCT
	rCTsIter                 int
	rateBeforeRCTs           float64
	eps                      float64
	sign                     float64
	state                    PCCState
	currRate                 float64
	prevUtility              float64
	adjustIter               int
	maxRateLimit             float64
	disableCongestionControl bool
	fixRateLimitMbits        int
	startRateMbits           int
	payloadSize              int
	rTT                      uint64
	pccMIDuration            float64
	miTxPackets              uint64
	miLoss                   uint64
	logger                   *zerolog.Logger
}

func (cc *CControlState) setupRCTs() {
	increaseFirst := rand.Intn(2)
	cc.rCTs = make([]RCT, RCTSIntervals)

	for i := 0; i < RCTSIntervals; i++ {
		sign := 1.0
		if i%2 == increaseFirst {
			sign = -1.0
		}
		cc.rCTs[i] = RCT{
			Rate:    cc.prevRate * (1.0 + sign*cc.eps),
			Utility: 0.0,
		}
	}

	for i := 0; i < RCTSIntervals; i++ {
		fmt.Println(cc.rCTs[i])
	}

	cc.rCTsIter = 0
	cc.rateBeforeRCTs = cc.prevRate
}

func (cc *CControlState) rctsDecision() RCTResult {
	fmt.Println("PCC RctsDecision")
	winningSign := 0.0

	for i := 0; i < len(cc.rCTs); i += 2 {
		if cc.rCTs[i].Utility > cc.rCTs[i+1].Utility {
			if cc.rCTs[i].Rate > cc.rCTs[i+1].Rate {
				winningSign++
			} else {
				winningSign--
			}
		} else if cc.rCTs[i].Utility < cc.rCTs[i+1].Utility {
			if cc.rCTs[i].Rate < cc.rCTs[i+1].Rate {
				winningSign++
			} else {
				winningSign--
			}
		}
	}

	switch {
	case winningSign > 0:
		fmt.Println("Increase")
		return Increase
	case winningSign < 0:
		fmt.Println("Decrease")
		return Decrease
	default:
		fmt.Println("Inconclusive")
		return Inconclusive
	}
}

func (ccState *CControlState) pccControlDecision(utility float64) float64 {
	fmt.Println("PCC Decision")
	if ccState.rCTsIter == -1 {
		ccState.setupRCTs()
		if ccState.rCTsIter != 0 {
			panic("RCTs iterator initialization failed")
		}
		ccState.state = PCCDecision
		return ccState.rCTs[ccState.rCTsIter].Rate
	}

	// Collect result
	ccState.rCTs[ccState.rCTsIter].Utility = utility

	if ccState.rCTsIter+1 < RCTSIntervals {
		ccState.rCTsIter++
		ccState.state = PCCDecision
		return ccState.rCTs[ccState.rCTsIter].Rate
	}

	// RCTs completed
	ccState.rCTsIter = -1
	decision := ccState.rctsDecision()
	if decision != Inconclusive {
		trialEps := ccState.eps
		ccState.eps = EpsMin // Reset eps
		ccState.state = PCCAdjust
		if decision == Increase {
			ccState.sign = 1.0
		} else if decision == Decrease {
			ccState.sign = -1.0
		}
		return ccState.rateBeforeRCTs * (1 + ccState.sign*trialEps)
	} else {
		// Return to previous rate and update eps
		ccState.eps = math.Min(ccState.eps+EpsMin, EpsMax)
		ccState.state = PCCDecision
		return ccState.rateBeforeRCTs
	}
}

func (cc *CControlState) pccControlAdjust(utility float64) float64 {
	fmt.Println("PCC Adjust")
	if utility > cc.prevUtility {
		n := cc.adjustIter
		sign := cc.sign
		cc.adjustIter++
		cc.state = PCCAdjust
		return cc.prevRate * (1.0 + sign*float64(n)*EpsMin)
	} else {
		cc.state = PCCDecision
		cc.adjustIter = 1
		return cc.prevRate
	}
}

func (cc *CControlState) pccControlStartup(utility, loss float64, prevRate float64) float64 {
	fmt.Println("PCC Startup")
	if utility > cc.prevUtility {
		fmt.Println("Utility > PrevUtility with ", utility, " and ", cc.prevUtility)
		cc.state = PCCStartup
		return 2 * cc.prevRate
	} else {
		cc.state = PCCDecision
		return prevRate // cc.PrevRate * (1 - loss)
	}
}

func (cc *CControlState) pccControl(throughput, loss float64) float64 {
	fmt.Println("PCC Control")
	if cc.state == PCCUninitialized || cc.state == PCCTerminated {
		return 0
	}

	prevRate := cc.prevRate
	cc.prevRate = cc.currRate
	utility := pccUtility(throughput, loss)
	newRate := cc.prevRate
	fmt.Println("Utility: ", utility)

	switch cc.state {
	case PCCStartup:
		newRate = cc.pccControlStartup(utility, loss, prevRate)
		fmt.Println("New Startup rate: ", newRate)
	case PCCDecision:
		newRate = cc.pccControlDecision(utility)
	case PCCAdjust:
		newRate = cc.pccControlAdjust(utility)
	default:
		fmt.Printf("Invalid PCC state: %d\n", cc.state)
		cc.state = PCCStartup
	}

	newRate = math.Min(math.Max(1, newRate), cc.maxRateLimit)
	cc.prevUtility = utility
	fmt.Println("Current rate: ", cc.currRate)
	fmt.Println("New rate: ", newRate)
	inMbits := newRate * float64(cc.payloadSize) * 8
	fmt.Println("New rate in Mbits: ", inMbits/1024/1024)
	cc.currRate = newRate
	return newRate
}

func pccUtility(throughput, loss float64) float64 {
	// Stub function; implement this based on your actual utility calculation logic
	return throughput*(1-loss)*sigmoid(loss-0.05) - throughput*loss
}

func sigmoid(x float64) float64 {
	alpha := 100.0 // Choose alpha appropriately
	return 1.0 / (1.0 + math.Exp(alpha*x))
}

func (cc *CControlState) startMonitoringInterval() {
	cc.limiter = ratelimit.New(int(cc.currRate))

	if cc.ticker != nil {
		cc.ticker.Stop()
	}
	fmt.Println("PCC Monitoring Interval")

	cc.ticker = time.NewTicker(time.Duration(cc.pccMIDuration) * time.Millisecond)
	go func() {
		for range cc.ticker.C {
			// Update stuff here
			fmt.Println("Monitoring interval")
			// Calculate throughput and loss
			fmt.Println("MiTxPackets: ", cc.miTxPackets, " MiLoss: ", cc.miLoss, " PccMIDuration: ", cc.pccMIDuration)
			throughput := float64(cc.miTxPackets) * float64(cc.payloadSize) / cc.pccMIDuration
			loss := float64(cc.miLoss) / float64(cc.miTxPackets)
			// loss = loss / 100
			fmt.Println("Throughput: ", throughput, " Loss: ", loss)
			// Update rate
			cc.currRate = cc.pccControl(throughput, loss)
			cc.limiter = ratelimit.New(int(cc.currRate))
			cc.miTxPackets = 0
			cc.miLoss = 0
		}
	}()

}

// umin32 returns the minimum of two uint32 values.
