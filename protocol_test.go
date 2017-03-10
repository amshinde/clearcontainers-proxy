// Copyright (c) 2016 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/clearcontainers/proxy/api"

	"github.com/stretchr/testify/assert"
)

// A simple way to mock a net.Conn around syscall.socketpair()
type mockServer struct {
	t                      *testing.T
	proto                  *protocol
	serverConn, clientConn net.Conn
}

func newMockServer(t *testing.T, proto *protocol) *mockServer {
	var err error

	server := &mockServer{
		t:     t,
		proto: proto,
	}

	server.serverConn, server.clientConn, err = Socketpair()
	assert.Nil(t, err)

	return server
}

func (server *mockServer) GetClientConn() net.Conn {
	return server.clientConn
}

func (server *mockServer) Serve() {
	server.ServeWithUserData(nil)
}

func (server *mockServer) ServeWithUserData(userData interface{}) {
	if err := server.proto.Serve(server.serverConn, userData); err != nil {
		server.serverConn.Close()
	}

}

func setupMockServer(t *testing.T, proto *protocol) (client net.Conn, server *mockServer) {
	server = newMockServer(t, proto)
	client = server.GetClientConn()
	go server.Serve()

	return client, server
}

// Test that we correctly give back the user data to handlers
type myUserData struct {
	t  *testing.T
	wg sync.WaitGroup
}

var testUserData myUserData

func userDataHandler(data []byte, userData interface{}, response *handlerResponse) {
	p := userData.(*myUserData)
	assert.Equal(p.t, p, &testUserData)

	p.wg.Done()
}

func TestUserData(t *testing.T) {
	proto := newProtocol()
	proto.Handle(api.Command(0), userDataHandler)

	server := newMockServer(t, proto)
	client := server.GetClientConn()
	testUserData.t = t
	go server.ServeWithUserData(&testUserData)

	testUserData.wg.Add(1)
	err := api.WriteCommand(client, api.Command(0), nil)
	assert.Nil(t, err)

	// make sure the handler runs by waiting for it
	testUserData.wg.Wait()
}

// Tests various behaviours of the protocol main loop and handler dispatching
func simpleHandler(data []byte, userData interface{}, response *handlerResponse) {
}

type Echo struct {
	Arg string
}

func echoHandler(data []byte, userData interface{}, response *handlerResponse) {
	echo := Echo{}
	json.Unmarshal(data, &echo)

	fmt.Fprintf(os.Stderr, "=== input %s\n", echo.Arg)
	response.AddResult("result", echo.Arg)
}

func returnDataHandler(data []byte, userData interface{}, response *handlerResponse) {
	response.AddResult("foo", "bar")
}

func returnErrorHandler(data []byte, userData interface{}, response *handlerResponse) {
	response.SetErrorMsg("This is an error")
}

func TestProtocol(t *testing.T) {
	tests := []struct {
		cmd   api.Command
		input string

		result bool
		output string
	}{
		{api.Command(0), "", true, ""},
		// Tests return values from handlers
		{api.Command(1), `{"arg": "bar"}`, true, `{"foo":"bar"}`},
		{api.Command(2), "", false, `{"msg":"This is an error"}`},
		// Tests we can unmarshal payload data
		{api.Command(3), `{"arg": "ping"}`, true, `{"result":"ping"}`},
	}

	proto := newProtocol()
	proto.Handle(api.Command(0), simpleHandler)
	proto.Handle(api.Command(1), returnDataHandler)
	proto.Handle(api.Command(2), returnErrorHandler)
	proto.Handle(api.Command(3), echoHandler)

	client, _ := setupMockServer(t, proto)

	for _, test := range tests {
		// request
		err := api.WriteCommand(client, test.cmd, []byte(test.input))
		assert.Nil(t, err)

		// response
		frame, err := api.ReadFrame(client)
		assert.Equal(t, frame.Header.InError, !test.result)
		assert.Nil(t, err)
		assert.NotNil(t, frame)
		assert.Equal(t, test.output, string(frame.Payload))
	}
}

// Make sure the server closes the connection when encountering an error
func TestCloseOnError(t *testing.T) {
	proto := newProtocol()
	proto.Handle(api.Command(0), simpleHandler)

	client, _ := setupMockServer(t, proto)

	// bad request
	err := api.WriteCommand(client, api.Command(255), nil)
	assert.Nil(t, err)

	// response
	buf := make([]byte, 512)
	_, err = client.Read(buf)
	assert.Equal(t, err, io.EOF)
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}
