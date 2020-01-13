// Package turn contains the public API for pion/turn, a toolkit for building TURN clients and servers
package turn

import (
	"fmt"
	"net"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v2/internal/allocation"
	"github.com/pion/turn/v2/internal/proto"
	"github.com/pion/turn/v2/internal/server"
)

const (
	inboundMTU = 1500
)

// Server is an instance of the Pion TURN Server
type Server struct {
	log                logging.LeveledLogger
	authHandler        AuthHandler
	realm              string
	channelBindTimeout time.Duration

	packetConnConfigs []PacketConnConfig
	listenerConfigs   []ListenerConfig
}

// NewServer creates the Pion TURN server
func NewServer(config ServerConfig) (*Server, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	loggerFactory := config.LoggerFactory
	if loggerFactory == nil {
		loggerFactory = logging.NewDefaultLoggerFactory()
	}

	s := &Server{
		log:                loggerFactory.NewLogger("turn"),
		authHandler:        config.AuthHandler,
		realm:              config.Realm,
		channelBindTimeout: config.ChannelBindTimeout,
		packetConnConfigs:  config.PacketConnConfigs,
		listenerConfigs:    config.ListenerConfigs,
	}

	if s.channelBindTimeout == 0 {
		s.channelBindTimeout = proto.DefaultLifetime
	}

	for _, p := range s.packetConnConfigs {
		go s.packetConnReadLoop(p.PacketConn, p.RelayAddressGenerator)
	}

	for _, listener := range s.listenerConfigs {
		go func(l ListenerConfig) {
			conn, err := l.Listener.Accept()
			if err != nil {
				s.log.Debugf("exit accept loop on error: %s", err.Error())
				return
			}

			go s.connReadLoop(conn, l.RelayAddressGenerator)
		}(listener)
	}

	return s, nil
}

// Close stops the TURN Server. It cleans up any associated state and closes all connections it is managing
func (s *Server) Close() error {
	var errors []error

	for _, p := range s.packetConnConfigs {
		if err := p.PacketConn.Close(); err != nil {
			errors = append(errors, err)
		}
	}

	for _, l := range s.listenerConfigs {
		if err := l.Listener.Close(); err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) == 0 {
		return nil
	}

	err := fmt.Errorf("Server failed to close")
	for _, e := range errors {
		err = fmt.Errorf("%w; Close error (%v) ", err, e)
	}

	return err
}

func (s *Server) connReadLoop(c net.Conn, r RelayAddressGenerator) {
	allocationManager, err := allocation.NewManager(allocation.ManagerConfig{
		AllocatePacketConn: r.AllocatePacketConn,
		AllocateConn:       r.AllocateConn,
		LeveledLogger:      s.log,
	})
	if err != nil {
		s.log.Errorf("exit read loop on error: %s", err.Error())
		return
	}
	defer func() {
		if err := allocationManager.Close(); err != nil {
			s.log.Errorf("Failed to close AllocationManager: %s", err.Error())
		}
	}()

	stunConn := NewSTUNConn(c)
	buf := make([]byte, inboundMTU)
	for {
		n, addr, err := stunConn.ReadFrom(buf)

		if err != nil {
			s.log.Debugf("exit read loop on error: %s", err.Error())
			return
		}

		if err := server.HandleRequest(server.Request{
			Conn:               stunConn,
			SrcAddr:            addr,
			Buff:               buf[:n],
			Log:                s.log,
			AuthHandler:        s.authHandler,
			Realm:              s.realm,
			AllocationManager:  allocationManager,
			ChannelBindTimeout: s.channelBindTimeout,
		}); err != nil {
			s.log.Errorf("error when handling datagram: %v", err)
		}
	}
}

func (s *Server) packetConnReadLoop(p net.PacketConn, r RelayAddressGenerator) {
	allocationManager, err := allocation.NewManager(allocation.ManagerConfig{
		AllocatePacketConn: r.AllocatePacketConn,
		AllocateConn:       r.AllocateConn,
		LeveledLogger:      s.log,
	})
	if err != nil {
		s.log.Errorf("exit read loop on error: %s", err.Error())
		return
	}
	defer func() {
		if err := allocationManager.Close(); err != nil {
			s.log.Errorf("Failed to close AllocationManager: %s", err.Error())
		}
	}()

	buf := make([]byte, inboundMTU)
	for {
		n, addr, err := p.ReadFrom(buf)

		if err != nil {
			s.log.Debugf("exit read loop on error: %s", err.Error())
			return
		}

		if err := server.HandleRequest(server.Request{
			Conn:               p,
			SrcAddr:            addr,
			Buff:               buf[:n],
			Log:                s.log,
			AuthHandler:        s.authHandler,
			Realm:              s.realm,
			AllocationManager:  allocationManager,
			ChannelBindTimeout: s.channelBindTimeout,
		}); err != nil {
			s.log.Errorf("error when handling datagram: %v", err)
		}
	}
}
