// Copyright 2014 Jovany Leandro G.C <bit4bit@riseup.net>. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file

// Package gami provites primitives for interacting with Asterisk AMI
package gami

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	errNoAMI         = errors.New("Server doesn`t have AMI interface")
	errNoEvent       = errors.New("No Event")
	errInvalidParams = errors.New("Invalid Params")
)

// Params for the actions
type Params map[string]string

// AMIClient a connection to AMI server
type AMIClient struct {
	conn             *textproto.Conn
	connRaw          io.ReadWriteCloser
	mutexAsyncAction *sync.RWMutex

	address     string
	amiUser     string
	amiPass     string
	useTLS      bool
	unsecureTLS bool

	// TLSConfig for secure connections
	tlsConfig *tls.Config

	// network wait for a new connection
	waitNewConnection chan struct{}

	response map[string]chan *AMIResponse

	// Events for client parse
	Events chan *AMIEvent

	// Error Raise on logic
	Error chan error

	//NetError a network error
	NetError chan error
}

// AMIResponse from action
type AMIResponse struct {
	ID     string
	Status string
	Params map[string]string
}

// AMIEvent it's a representation of Event readed
type AMIEvent struct {
	//Identification of event Event: xxxx
	ID        string
	Privilege []string
	// Params  of arguments received
	Params map[string]string
}

//UseTLS
func UseTLS(c *AMIClient) {
	c.useTLS = true
}

//UseTLSConfig
func UseTLSConfig(config *tls.Config) func(*AMIClient) {
	return func(c *AMIClient) {
		c.tlsConfig = config
		c.useTLS = true
	}
}

//UnsecureTLS
func UnsecureTLS(c *AMIClient) {
	c.unsecureTLS = true
}

// Login authenticate to AMI
func (client *AMIClient) Login(username, password string) error {
	response, _, err := client.Action(Params{"Action": "Login", "Username": username, "Secret": password})
	if err != nil {
		return err
	}

	resp := <-response
	if resp.Status == "Error" {
		return errors.New(resp.Params["Message"])
	}

	client.amiUser = username
	client.amiPass = password

	return nil
}

// Reconnect the session, autologin if a new network error it put on client.NetError
func (client *AMIClient) Reconnect() error {
	client.conn.Close()

	err := client.NewConn()

	if err != nil {
		client.NetError <- err
		return err
	}

	client.waitNewConnection <- struct{}{}

	if err := client.Login(client.amiUser, client.amiPass); err != nil {
		return err
	}

	return nil
}

// Action return chan for wait response of action with parameter *ActionID* this can be helpful for
// massive actions,
func (client *AMIClient) Action(p Params) (<-chan *AMIResponse, string, error) {
	client.mutexAsyncAction.Lock()
	defer client.mutexAsyncAction.Unlock()

	if p == nil {
		return nil, "", errInvalidParams
	}

	client.normaliser(&p)

	if _, ok := p["Action"]; !ok {
		return nil, "", errInvalidParams
	}

	if _, ok := client.response[p["Actionid"]]; !ok {
		client.response[p["Actionid"]] = make(chan *AMIResponse, 1)
	}

	var output string
	for k, v := range p {
		output += fmt.Sprintf("%s: %s\r\n", k, v)
	}

	if err := client.conn.PrintfLine("%s", output); err != nil {
		return nil, "", err
	}

	return client.response[p["Actionid"]], p["Actionid"], nil
}

// Run process socket waiting events and responses
func (client *AMIClient) Run() {
	go func() {
		for {
			data, err := client.conn.ReadMIMEHeader()
			if err != nil {
				switch err {
				case syscall.ECONNABORTED:
					fallthrough
				case syscall.ECONNRESET:
					fallthrough
				case syscall.ECONNREFUSED:
					fallthrough
				case io.EOF:
					client.NetError <- err
					<-client.waitNewConnection
				default:
					client.Error <- err
				}
				continue
			}

			if ev, err := newEvent(&data); err != nil {
				if err != errNoEvent {
					client.Error <- err
				}
			} else {
				client.Events <- ev
			}

			//only handle valid responses
			//@todo handle longs response
			// see  https://marcelog.github.io/articles/php_asterisk_manager_interface_protocol_tutorial_introduction.html
			if response, err := newResponse(&data); err == nil {
				client.notifyResponse(response)
			}

		}
	}()
}

// Close the connection to AMI
func (client *AMIClient) Close() {
	client.Action(Params{"Action": "Logoff"})
	(client.connRaw).Close()
}

func (client *AMIClient) notifyResponse(response *AMIResponse) {
	go func() {
		client.mutexAsyncAction.RLock()
		client.response[response.ID] <- response
		close(client.response[response.ID])
		client.mutexAsyncAction.RUnlock()

		client.mutexAsyncAction.Lock()
		delete(client.response, response.ID)
		client.mutexAsyncAction.Unlock()
	}()
}

//newResponse build a response for action
func newResponse(data *textproto.MIMEHeader) (*AMIResponse, error) {
	if data.Get("Response") == "" {
		return nil, errors.New("Not Response")
	}

	response := &AMIResponse{data.Get("Actionid"),
		data.Get("Response"),
		make(map[string]string)}

	for k, v := range *data {
		if k == "Response" {
			continue
		}
		response.Params[k] = v[0]
	}
	return response, nil
}

//newEvent build event
func newEvent(data *textproto.MIMEHeader) (*AMIEvent, error) {
	if data.Get("Event") == "" {
		return nil, errNoEvent
	}
	ev := &AMIEvent{data.Get("Event"),
		strings.Split(data.Get("Privilege"), ","),
		make(map[string]string)}

	for k, v := range *data {
		if k == "Event" || k == "Privilege" {
			continue
		}
		ev.Params[k] = v[0]
	}
	return ev, nil
}

// Dial create a new connection to AMI
func Dial(address string, options ...func(*AMIClient)) (*AMIClient, error) {
	client := &AMIClient{
		address:           address,
		amiUser:           "",
		amiPass:           "",
		mutexAsyncAction:  new(sync.RWMutex),
		waitNewConnection: make(chan struct{}),
		response:          make(map[string]chan *AMIResponse),
		Events:            make(chan *AMIEvent, 100),
		Error:             make(chan error, 1),
		NetError:          make(chan error, 1),
		useTLS:            false,
		unsecureTLS:       false,
		tlsConfig:         new(tls.Config),
	}
	for _, op := range options {
		op(client)
	}
	err := client.NewConn()
	if err != nil {
		return nil, err
	}
	return client, nil
}

// NewConn create a new connection to AMI
func (client *AMIClient) NewConn() (err error) {
	if client.useTLS {
		client.tlsConfig.InsecureSkipVerify = client.unsecureTLS
		client.connRaw, err = tls.Dial("tcp", client.address, client.tlsConfig)
	} else {
		client.connRaw, err = net.Dial("tcp", client.address)
	}

	if err != nil {
		return err
	}

	client.conn = textproto.NewConn(client.connRaw)
	label, err := client.conn.ReadLine()
	if err != nil {
		return err
	}

	if strings.Contains(label, "Asterisk Call Manager") != true {
		return errNoAMI
	}

	return nil
}

func (client *AMIClient) normaliser(p *Params) {
	fixp := make(Params)
	for k, v := range *p {
		delete(*p, k)
		fixp[strings.Title(strings.ToLower(k))] = strings.TrimSpace(v)
	}

	if _, ok := fixp["Actionid"]; !ok {
		fixp["Actionid"] = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	*p = fixp
}
