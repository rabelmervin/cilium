// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	gobgpapi "github.com/osrg/gobgp/v3/api"
	"github.com/osrg/gobgp/v3/pkg/apiutil"
	gobgpb "github.com/osrg/gobgp/v3/pkg/packet/bgp"
	"github.com/osrg/gobgp/v3/pkg/server"

	"github.com/cilium/cilium/pkg/bgpv1/gobgp"
	"github.com/cilium/cilium/pkg/bgpv1/types"
	"github.com/cilium/cilium/pkg/logging/logfields"
)

// goBGP configuration used in tests
var (
	gobgpASN         = uint32(65011)
	gobgpASN2        = uint32(65012)
	gobgpListenPort  = int32(1791)
	gobgpListenPort2 = int32(1792)

	gobgpGlobal = &gobgpapi.Global{
		Asn:        gobgpASN,
		RouterId:   dummies[instance1Link].ipv4.Addr().String(),
		ListenPort: gobgpListenPort,
	}
	gobgpGlobal2 = &gobgpapi.Global{
		Asn:        gobgpASN2,
		RouterId:   dummies[instance1Link].ipv4.Addr().String(),
		ListenPort: gobgpListenPort2,
	}
	gobgpGlobalIBGP = &gobgpapi.Global{
		Asn:        ciliumASN, // iBGP
		RouterId:   dummies[instance1Link].ipv4.Addr().String(),
		ListenPort: gobgpListenPort,
	}

	gbgpNeighConf = &gobgpapi.Peer{
		Conf: &gobgpapi.PeerConf{
			NeighborAddress: dummies[ciliumLink].ipv4.Addr().String(),
			PeerAsn:         ciliumASN,
		},
		Transport: &gobgpapi.Transport{
			RemoteAddress: dummies[ciliumLink].ipv4.Addr().String(),
			RemotePort:    ciliumListenPort,
			LocalAddress:  dummies[instance1Link].ipv4.Addr().String(),
			PassiveMode:   false,
		},
		AfiSafis: []*gobgpapi.AfiSafi{
			{
				Config: &gobgpapi.AfiSafiConfig{
					Family: gobgp.GoBGPIPv4Family,
				},
			},
			{
				Config: &gobgpapi.AfiSafiConfig{
					Family: gobgp.GoBGPIPv6Family,
				},
			},
		},
	}
	gbgpNeighConf2 = &gobgpapi.Peer{
		Conf: &gobgpapi.PeerConf{
			NeighborAddress: dummies[ciliumLink].ipv4.Addr().String(),
			PeerAsn:         ciliumASN,
		},
		Transport: &gobgpapi.Transport{
			RemoteAddress: dummies[ciliumLink].ipv4.Addr().String(),
			RemotePort:    ciliumListenPort,
			LocalAddress:  dummies[instance2Link].ipv4.Addr().String(),
			PassiveMode:   false,
		},
		AfiSafis: []*gobgpapi.AfiSafi{
			{
				Config: &gobgpapi.AfiSafiConfig{
					Family: gobgp.GoBGPIPv4Family,
				},
			},
			{
				Config: &gobgpapi.AfiSafiConfig{
					Family: gobgp.GoBGPIPv6Family,
				},
			},
		},
	}
	gbgpNeighConfPassword = &gobgpapi.Peer{
		Conf: &gobgpapi.PeerConf{
			NeighborAddress: dummies[ciliumLink].ipv4.Addr().String(),
			PeerAsn:         ciliumASN,
			AuthPassword:    "testing-123",
		},
		Transport: &gobgpapi.Transport{
			RemoteAddress: dummies[ciliumLink].ipv4.Addr().String(),
			RemotePort:    ciliumListenPort,
			LocalAddress:  dummies[instance1Link].ipv4.Addr().String(),
			PassiveMode:   false,
		},
		AfiSafis: []*gobgpapi.AfiSafi{
			{
				Config: &gobgpapi.AfiSafiConfig{
					Family: gobgp.GoBGPIPv4Family,
				},
			},
			{
				Config: &gobgpapi.AfiSafiConfig{
					Family: gobgp.GoBGPIPv6Family,
				},
			},
		},
	}

	gobgpConf = gobgpConfig{
		global: gobgpGlobal,
		neighbors: []*gobgpapi.Peer{
			gbgpNeighConf,
		},
	}
	gobgpConf2 = gobgpConfig{
		global: gobgpGlobal2,
		neighbors: []*gobgpapi.Peer{
			gbgpNeighConf2,
		},
	}
	gobgpConfPassword = gobgpConfig{
		global: gobgpGlobal,
		neighbors: []*gobgpapi.Peer{
			gbgpNeighConfPassword,
		},
	}
	gobgpConfIBGP = gobgpConfig{
		global: gobgpGlobalIBGP,
		neighbors: []*gobgpapi.Peer{
			gbgpNeighConf,
		},
	}
)

// gobgpConfig used for starting gobgp instance
type gobgpConfig struct {
	global    *gobgpapi.Global
	neighbors []*gobgpapi.Peer
}

// routeEvent contains information about new event in routing table of gobgp
type routeEvent struct {
	sourceASN           uint32
	prefix              string
	prefixLen           uint8
	isWithdrawn         bool
	extraPathAttributes []gobgpb.PathAttributeInterface // non-standard path attributes (other than Origin / ASPath / NextHop / MpReachNLRI)
}

// peerEvent contains information about peer state change of gobgp
type peerEvent struct {
	peerASN uint32
	state   string
}

// goBGP wrapper on gobgp server and provides route and peer event handling
type goBGP struct {
	context context.Context
	logger  *slog.Logger

	server      *server.BgpServer
	peerEvents  chan *gobgpapi.WatchEventResponse_PeerEvent
	tableEvents chan *gobgpapi.WatchEventResponse_TableEvent

	peerNotif  chan peerEvent
	routeNotif chan routeEvent
}

// startGoBGP initialized new gobgp server, configures neighbors and starts listening on route and peer events
func startGoBGP(ctx context.Context, logger *slog.Logger, conf gobgpConfig) (g *goBGP, err error) {
	g = &goBGP{
		context: ctx,
		logger:  logger,
		server: server.NewBgpServer(server.LoggerOption(gobgp.NewServerLogger(logger, gobgp.LogParams{
			AS:        conf.global.Asn,
			Component: "tests.BGP",
			SubSys:    "basic",
		}))),
		peerEvents:  make(chan *gobgpapi.WatchEventResponse_PeerEvent),
		tableEvents: make(chan *gobgpapi.WatchEventResponse_TableEvent),
		peerNotif:   make(chan peerEvent),
		routeNotif:  make(chan routeEvent),
	}

	go g.server.Serve()
	go g.readEvents()

	// in case of err, clean up
	defer func() {
		if err != nil {
			g.server.Stop()
		}
	}()

	g.logger.Info("GoBGP test instance: starting")
	err = g.server.StartBgp(ctx, &gobgpapi.StartBgpRequest{Global: conf.global})
	if err != nil {
		return
	}

	// register watchers for peer and route events
	watchRequest := &gobgpapi.WatchEventRequest{
		Peer: &gobgpapi.WatchEventRequest_Peer{},
		Table: &gobgpapi.WatchEventRequest_Table{
			Filters: []*gobgpapi.WatchEventRequest_Table_Filter{
				{
					Type: gobgpapi.WatchEventRequest_Table_Filter_BEST,
					Init: true,
				},
			},
		},
	}

	err = g.server.WatchEvent(ctx, watchRequest, func(r *gobgpapi.WatchEventResponse) {
		switch r.Event.(type) {
		case *gobgpapi.WatchEventResponse_Peer:
			g.peerEvents <- r.GetPeer()
		case *gobgpapi.WatchEventResponse_Table:
			g.tableEvents <- r.GetTable()
		}
	})
	if err != nil {
		return
	}

	// configure neighbors
	for _, peer := range conf.neighbors {
		err = g.server.AddPeer(ctx, &gobgpapi.AddPeerRequest{Peer: peer})
		if err != nil {
			return
		}
	}

	return
}

// stopGoBGP stops server
func (g *goBGP) stopGoBGP() {
	g.logger.Info("GoBGP test instance: stopping")
	g.server.Stop()
}

// readEvents receives peer and route events from gobgp callbacks, unmarshal response to well-defined structs and
// pass this to consumers of peer and route events.
// Note this will block if there is no consumer reading, in which case test context would timeout resulting in termination
// of this goroutine.
func (g *goBGP) readEvents() {
	for {
		select {
		case e := <-g.tableEvents:
			for _, p := range e.Paths {
				var prefix string
				var length uint8

				nlri, err := apiutil.UnmarshalNLRI(gobgpb.AfiSafiToRouteFamily(uint16(p.Family.Afi), uint8(p.Family.Safi)), p.Nlri)
				if err != nil {
					g.logger.Error("failed to unmarshal path nlri",
						logfields.Error, err,
						types.PathLogField, p,
					)
					continue
				}

				switch a := nlri.(type) {
				case *gobgpb.IPAddrPrefix:
					prefix = a.Prefix.String()
					length = a.Length
				case *gobgpb.IPv6AddrPrefix:
					prefix = a.Prefix.String()
					length = a.Length
				default:
					g.logger.Error("failed to identify nlri", types.NLRILogField, nlri)
					continue
				}

				pattrs, err := apiutil.UnmarshalPathAttributes(p.Pattrs)
				if err != nil {
					g.logger.Error("failed to unmarshal path attributes",
						logfields.Error, err,
						types.PathLogField, p,
					)
					continue
				}

				select {
				case g.routeNotif <- routeEvent{
					sourceASN:           p.SourceAsn,
					prefix:              prefix,
					prefixLen:           length,
					isWithdrawn:         p.IsWithdraw,
					extraPathAttributes: g.filterStandardPathAttributes(pattrs),
				}:
				case <-g.context.Done():
					return
				}
			}

		case e := <-g.peerEvents:
			if e.Peer != nil {
				select {
				case g.peerNotif <- peerEvent{
					peerASN: e.Peer.Conf.PeerAsn,
					state:   e.Peer.State.SessionState.String(),
				}:
				case <-g.context.Done():
					return
				}
			}

		case <-g.context.Done():
			return
		}
	}
}

// waitForSessionState consumes state changes from gobgp and compares it with expected states
func (g *goBGP) waitForSessionState(ctx context.Context, expectedStates []string) error {
	for {
		select {
		case e := <-g.peerNotif:
			g.logger.Info("GoBGP test instance", types.PeerEventLogField, e)

			if slices.Contains(expectedStates, e.state) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("did not receive expected peering state %q: %w", expectedStates, ctx.Err())
		}
	}
}

// getRouteEvents drains number of events from routeNotif chan and return those events to caller.
func (g *goBGP) getRouteEvents(ctx context.Context, numExpectedEvents int) ([]routeEvent, error) {
	var receivedEvents []routeEvent

	for range numExpectedEvents {
		select {
		case r := <-g.routeNotif:
			g.logger.Info("GoBGP test instance", types.RouteLogField, r)
			receivedEvents = append(receivedEvents, r)
		case <-ctx.Done():
			return receivedEvents, fmt.Errorf("time elapsed waiting for all route events - received %d, expected %d : %w",
				len(receivedEvents), numExpectedEvents, ctx.Err())
		}
	}

	return receivedEvents, nil
}

// filterStandardPathAttributes filters standard path attributes (usually present on all routes) from the
// provided list of the path attributes.
func (g *goBGP) filterStandardPathAttributes(attrs []gobgpb.PathAttributeInterface) []gobgpb.PathAttributeInterface {
	var res []gobgpb.PathAttributeInterface
	for _, a := range attrs {
		switch a.(type) {
		case *gobgpb.PathAttributeOrigin:
			continue
		case *gobgpb.PathAttributeAsPath:
			continue
		case *gobgpb.PathAttributeNextHop:
			continue
		case *gobgpb.PathAttributeMpReachNLRI:
			continue
		}
		res = append(res, a)
	}
	return res
}
