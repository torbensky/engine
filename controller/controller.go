// Package controller provides an API available to workers to write games. It
// also provides the internal API for starting games and watching.
package controller

import (
	"context"
	"fmt"
	"net"

	"github.com/battlesnakeio/engine/controller/pb"
	"github.com/battlesnakeio/engine/rules"
	"github.com/battlesnakeio/engine/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MaxTicks is the maximum amount of ticks that can be returned.
const MaxTicks = 100

// New will initialize a new Server.
func New(store Store) *Server {
	return &Server{
		Store:   store,
		started: make(chan struct{}),
	}
}

// Server is a grpc server for pb.ControllerServer.
type Server struct {
	Store Store

	started chan struct{}
	port    int
}

// Pop should pop a game that is unlocked and unfinished from the queue, lock
// the game and return it to the worker to begin processing. This call will
// be polled by the workers.
func (s *Server) Pop(ctx context.Context, _ *pb.PopRequest) (*pb.PopResponse, error) {
	id, err := s.Store.PopGameID(ctx)
	if err != nil {
		return nil, err
	}

	token, err := s.Store.Lock(ctx, id, "")
	if err != nil {
		return nil, err
	}

	return &pb.PopResponse{ID: id, Token: token}, nil
}

// Status retrieves the game state including the last processed game frame.
func (s *Server) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	game, err := s.Store.GetGame(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	var lastFrame *pb.GameFrame
	frames, err := s.Store.ListGameFrames(ctx, req.ID, 1, -1)
	if err != nil {
		return nil, err
	}
	if len(frames) > 0 {
		lastFrame = frames[0]
	}
	return &pb.StatusResponse{Game: game, LastFrame: lastFrame}, nil
}

// Start starts the game running, and will make it ready to be picked up by a
// worker.
func (s *Server) Start(ctx context.Context, req *pb.StartRequest) (*pb.StartResponse, error) {
	err := s.Store.SetGameStatus(ctx, req.ID, rules.GameStatusRunning)
	if err != nil {
		return nil, err
	}
	return &pb.StartResponse{}, nil
}

// Create creates a new game, but doesn't start running frames.
func (s *Server) Create(ctx context.Context, req *pb.CreateRequest) (*pb.CreateResponse, error) {
	game, frames, err := rules.CreateInitialGame(req)
	if err != nil {
		return nil, err
	}
	err = s.Store.CreateGame(ctx, game, frames)
	if err != nil {
		return nil, err
	}
	return &pb.CreateResponse{
		ID: game.ID,
	}, nil
}

// AddGameFrame adds a new game frame to the game. A lock must be held for this
// call to succeed.
func (s *Server) AddGameFrame(ctx context.Context, req *pb.AddGameFrameRequest) (*pb.AddGameFrameResponse, error) {
	token := pb.ContextGetLockToken(ctx)

	if req.GameFrame == nil {
		return nil, status.Error(codes.InvalidArgument, "controller: game frame must not be nil")
	}

	// Lock the game again, if this fails, the lock is not valid.
	_, err := s.Store.Lock(ctx, req.ID, token)
	if err != nil {
		return nil, err
	}

	err = s.Store.PushGameFrame(ctx, req.ID, req.GameFrame)
	if err != nil {
		return nil, err
	}
	game, err := s.Store.GetGame(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return &pb.AddGameFrameResponse{
		Game: game,
	}, nil
}

// ListGameFrames will list all game frames given a limit and offset.
func (s *Server) ListGameFrames(ctx context.Context, req *pb.ListGameFramesRequest) (*pb.ListGameFramesResponse, error) {
	if req.Limit == 0 || req.Limit >= MaxTicks {
		req.Limit = MaxTicks
	}
	frames, err := s.Store.ListGameFrames(ctx, req.ID, int(req.Limit), int(req.Offset))
	if err != nil {
		return nil, err
	}
	return &pb.ListGameFramesResponse{
		Frames: frames,
		Count:  int32(len(frames)),
	}, nil
}

// EndGame sets the game status to complete. A lock must be held for this call
// to succeed.
func (s *Server) EndGame(ctx context.Context, req *pb.EndGameRequest) (*pb.EndGameResponse, error) {
	token := pb.ContextGetLockToken(ctx)

	// Lock the game again, if this fails, the lock is not valid. We only need
	// the lock for the next part where we set the game status.
	newToken, err := s.Store.Lock(ctx, req.ID, token)
	if err != nil {
		return nil, err
	}
	token = newToken

	err = s.Store.SetGameStatus(ctx, req.ID, rules.GameStatusComplete)
	if err != nil {
		return nil, err
	}

	err = s.Store.Unlock(ctx, req.ID, token)
	if err != nil {
		return nil, err
	}

	return &pb.EndGameResponse{}, nil
}

// Ping returns the health and current version of the server.
func (s *Server) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{Version: version.Version}, nil
}

// Serve will intantiate a grpc server.
func (s *Server) Serve(listen string) error {
	lis, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	s.port = lis.Addr().(*net.TCPAddr).Port
	srv := grpc.NewServer()
	pb.RegisterControllerServer(srv, s)
	close(s.started)
	return srv.Serve(lis)
}

// DialAddress will return a localhost address to reach the server. This is
// useful if the server will select it's own port.
func (s *Server) DialAddress() string {
	s.Wait()
	return fmt.Sprintf("127.0.0.1:%d", s.port)
}

// Wait will wait until the server has started.
func (s *Server) Wait() { <-s.started }
