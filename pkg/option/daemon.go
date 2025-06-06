// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package option

import "maps"

var (
	specPolicyTracing = Option{
		Description: "Enable tracing when resolving policy (Debug)",
	}

	// DaemonOptionLibrary is the daemon's option library that should be
	// used for read-only.
	DaemonOptionLibrary = OptionLibrary{
		PolicyTracing: &specPolicyTracing,
	}

	DaemonMutableOptionLibrary = OptionLibrary{
		ConntrackAccounting:  &specConntrackAccounting,
		PolicyAccounting:     &specPolicyAccounting,
		Debug:                &specDebug,
		DebugLB:              &specDebugLB,
		DebugPolicy:          &specDebugPolicy,
		DropNotify:           &specDropNotify,
		TraceNotify:          &specTraceNotify,
		PolicyVerdictNotify:  &specPolicyVerdictNotify,
		PolicyAuditMode:      &specPolicyAuditMode,
		MonitorAggregation:   &specMonitorAggregation,
		SourceIPVerification: &specSourceIPVerification,
	}
)

func init() {
	maps.Copy(DaemonOptionLibrary, DaemonMutableOptionLibrary)
}

// ParseDaemonOption parses a string as daemon option
func ParseDaemonOption(opt string) (string, OptionSetting, bool, error) {
	return DaemonOptionLibrary.ParseOption(opt)
}
