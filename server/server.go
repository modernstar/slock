package server

import (
    "fmt"
    "net"
    "sync"
    "io"
    "github.com/snower/slock/protocol"
)

type Server struct {
    server  net.Listener
    streams []*Stream
    slock   *SLock
    glock   sync.Mutex
}

func NewServer(slock *SLock) *Server {
    return &Server{nil, make([]*Stream, 0), slock, sync.Mutex{}}
}

func (self *Server) Listen() error {
    addr := fmt.Sprintf("%s:%d", Config.Bind, Config.Port)
    server, err := net.Listen("tcp", addr)
    if err != nil {
        return err
    }
    self.server = server
    return nil
}

func (self *Server) AddStream(stream *Stream) (err error) {
    defer self.glock.Unlock()
    self.glock.Lock()
    self.streams = append(self.streams, stream)
    return nil
}

func (self *Server) RemoveStream(stream *Stream) (err error) {
    defer self.glock.Unlock()
    self.glock.Lock()
    streams := self.streams
    self.streams = make([]*Stream, len(streams))
    for i, v := range streams {
        if stream != v {
            self.streams[i] = v
        }
    }
    return nil
}

func (self *Server) Loop() {
    addr := fmt.Sprintf("%s:%d", Config.Bind, Config.Port)
    self.slock.Log().Infof("start server %s", addr)
    for {
        conn, err := self.server.Accept()
        if err != nil {
            continue
        }
        stream := NewStream(self, conn)
        if self.AddStream(stream) == nil {
            go self.Handle(stream)
        }
    }
}

func (self *Server) Handle(stream *Stream) {
    server_protocol := NewServerProtocol(self.slock, stream)
    defer func() {
        err := server_protocol.Close()
        if err != nil {
            self.slock.Log().Infof("server protocol close error: %v", err)
        }
    }()

    for {
        command, err := server_protocol.Read()
        if err != nil {
            if err != io.EOF {
                self.slock.Log().Infof("read command error: %v", err)
            }
            break
        }

        if command == nil {
            self.slock.Log().Infof("read command decode error", err)
            break
        }

        err = self.slock.Handle(server_protocol, command.(protocol.ICommand))
        if err != nil {
            self.slock.Log().Infof("slock handle command error", err)
            break
        }
    }
}
