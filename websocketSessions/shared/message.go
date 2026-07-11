package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"strings"
	"time"
)

const (
	MessageTypeSignup = iota
	MessageTypeError  //1
	MessageTypeWrite
)

var (
	ErrGetMessageTimeout = errors.New("timeout")
	ErrShouldNeverHappen = errors.New("should never happen")
)

type RfidReaderName string

type SocketMessage struct {
	Type int    `json:"type"` // Always binary, ping, pong, or string(error)
	Data []byte `json:"data,omitempty"`
}

func (sockMsg *SocketMessage) ValidateReadRequest() error {
	if sockMsg.Type != ReadRequestType {
		return errors.New("invalid type on read request")
	}
	if len(sockMsg.Data) != 1 {
		return errors.New("read request must contain only 1 byte")
	}
	if sockMsg.Data[0] != FirstByteRead {
		return errors.New("incorrect first byte on read request")
	}
	return nil
}

func (sockMsg *SocketMessage) String(expectedType ...int) string {
	b := strings.Builder{}
	b.Write([]byte(fmt.Sprintf("\nType: %d", sockMsg.Type)))
	//if len(expectedType) != 0 {
	//	b.Write([]byte(fmt.Sprintf(". Exp: %d\n", expectedType[0]))) // TODO: del?
	//}
	if sockMsg.Data != nil {
		if len(sockMsg.Data) != 0 {
			b.Write([]byte(fmt.Sprintf("BytesWithFirst:%s\n", string(sockMsg.Data))))
			if len(sockMsg.Data) > 1 {
				b.Write([]byte(fmt.Sprintf("BytesW/otFirst:%s\n", string(sockMsg.Data[1:]))))
			}
		}
	}
	return b.String()
}
func (sockMsg *SocketMessage) ValidateWriteRequest() ([RfidByteSize]byte, error) {
	if sockMsg.Type != WriteRequestType {
		return [RfidByteSize]byte{}, errors.New("invalid type on write request")
	}
	if len(sockMsg.Data) != 1+RfidByteSize {
		return [RfidByteSize]byte{}, errors.New("invalidly sized write request")
	}
	if sockMsg.Data[0] != FirstByteWrite {
		return [RfidByteSize]byte{}, errors.New("incorrect first byte on write request")
	}
	result := [RfidByteSize]byte(sockMsg.Data[1:])
	return result, nil
}

func (sockMsg *SocketMessage) WithData(data []byte) *SocketMessage {
	sockMsg.Data = data
	return sockMsg
}
func (sockMsg *SocketMessage) WithType(msgType int) *SocketMessage {
	sockMsg.Type = msgType
	return sockMsg
}
func (sockMsg *SocketMessage) WriteTo(c *websocket.Conn) error {
	//println("Attempting to write message:" + sockMsg.String())                  // TODO: del
	if err := c.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		println("failed to set write deadline: " + err.Error())
		return err
	}
	err := c.WriteMessage(sockMsg.Type, sockMsg.Data)
	if err != nil {
		return errors.Join(errors.New("failed to write socket message to connection"), err)
	}
	return nil
}

const (
	FirstByteRead uint8 = iota
	FirstByteWrite
)

type SignupRequest struct {
	Name   RfidReaderName
	Secret string
}

func NewErrorResponse(err error) *SocketMessage {
	return &SocketMessage{
		Type: websocket.TextMessage,
		Data: []byte(err.Error()),
	}
}

const ReadRequestType = websocket.BinaryMessage
const ReadResponseType = websocket.BinaryMessage
const WriteRequestType = websocket.BinaryMessage
const WriteResponseType = websocket.BinaryMessage

func NewReadRequest() *SocketMessage {
	return &SocketMessage{
		Type: ReadRequestType,
		Data: []byte{FirstByteRead},
	}
}
func NewReadResponse(r [RfidByteSize]byte) *SocketMessage {
	return &SocketMessage{
		Type: ReadResponseType,
		Data: r[:],
	}
}
func NewWriteRequest(w [RfidByteSize]byte) *SocketMessage {
	bs := make([]byte, 0, RfidByteSize+1)
	bs = append(bs, FirstByteWrite)
	bs = append(bs, w[:]...)
	return &SocketMessage{
		Type: WriteRequestType,
		Data: bs,
	}
}
func NewWriteResponse(w [RfidByteSize]byte) *SocketMessage {
	return &SocketMessage{
		Type: WriteResponseType,
		Data: w[:],
	}
}

type SignupResponse struct {
	ClientName string `json:"clientName"`
}

var (
	ErrSignupResponseNil = errors.New("Signup response socket message was nil")
	ErrSignupReqNil      = errors.New("Signup request socket message was nil")
)

func (resp *SignupResponse) Validate(expectedName string) error {
	if resp.ClientName != expectedName {
		return fmt.Errorf(`Expected name %s, received name %s`, expectedName, resp.ClientName)
	}
	return nil
}
func (msg *SocketMessage) ParseSignupResponse() (out *SignupResponse, err error) {
	if msg == nil {
		return nil, ErrSignupResponseNil
	}
	if msg.Type != SignupResponseType {
		return nil, fmt.Errorf(`2 Signup response type invalid, should be text (%d), was %d`, SignupResponseType, msg.Type)
	}
	return &SignupResponse{ClientName: string(msg.Data)}, nil
}
func (msg *SocketMessage) ParseSignupRequest() (out *SignupRequest, err error) {
	if msg == nil {
		return nil, ErrSignupReqNil

	}
	if msg.Type != SignupRequestType {
		return nil, fmt.Errorf(`1 Signup request type invalid, should be (%d), was %d`, SignupRequestType, msg.Type)
	}
	out = &SignupRequest{}
	err = json.Unmarshal(msg.Data, out)
	return out, err
}

const SignupRequestType = websocket.BinaryMessage
const SignupResponseType = websocket.TextMessage

func NewSignupRequest(readerName, secret string) *SocketMessage {
	bs, err := json.Marshal(SignupRequest{
		Name:   RfidReaderName(readerName),
		Secret: secret,
	})
	if err != nil {
		panic("failed to create signup request: " + err.Error()) // TODO: ew
	}
	return &SocketMessage{
		Type: SignupRequestType,
		Data: bs,
	}
}
func NewSignupResponse(clientName RfidReaderName) *SocketMessage {
	return &SocketMessage{
		Type: SignupResponseType,
		Data: []byte(clientName),
	}
}
func NewRenewalRequest(readerName string) *SocketMessage {
	return &SocketMessage{
		Type: websocket.PingMessage,
		Data: []byte(readerName),
	}
}
func NewRenewalResponse(secret string) *SocketMessage {
	return &SocketMessage{
		Type: websocket.PongMessage,
		Data: []byte(secret),
	}
}

//func (res ReceivedMsg) ValidateRenewalRequest(expName string) error {
//	return res.genericValidate(websocket.PingMessage, FirstByteRenew, expName, "renewal request")
//}

//func (res ReceivedMsg) ValidateRenewalResponse(expSecret string) error {
//	return res.genericValidate(websocket.PongMessage, FirstByteRenew, expSecret, "renewal response")
//}

//func (res ReceivedMsg) genericValidate(expMsgType int, expFirstByte uint8, exStr string, what string) error {
//	resp, err := res.GetRequestData(expMsgType, expFirstByte)
//	if err != nil {
//		return err
//	}
//	if string(resp) != exStr {
//		return fmt.Errorf(`received incorrect secret on %s`, what)
//	}
//	return nil
//}

func TryGetMessage(ctx context.Context, conn *websocket.Conn, timeout ...time.Duration) ReceivedMsg {
	timeoutActual := 30 * time.Second // TODO: default ok?
	if len(timeout) > 0 {
		timeoutActual = timeout[0]
	}
	ctxWithCancel, cancel := context.WithTimeout(ctx, timeoutActual)
	defer cancel()

	resultChan := make(chan ReceivedMsg, 1)
	go func() {
		defer close(resultChan)                   // TODO: or in gofunc?
		msgType, bytes, err := conn.ReadMessage() // Bytes may include first bytes to define type              // TODO: this will generally be binary or text for non-signup-flow items
		if err != nil {
			println("failed to read from connection... " + err.Error()) // TODO: del?
			return                                                      // TODO: ok?
		}
		recvd := ReceivedMsg{msgType, bytes, err}
		println("Received message:" + recvd.String()) // TODO: del!
		resultChan <- recvd
	}()
	select {
	case res := <-resultChan:
		return res
	case <-ctxWithCancel.Done():
		return ReceivedMsg{
			MsgType: MessageTypeError,
			Bytes:   nil,
			Err:     ErrGetMessageTimeout,
		}
	}
}

type ReceivedMsg struct {
	MsgType int
	Bytes   []byte
	Err     error
}

func (res ReceivedMsg) AsSignupResponse(expName string) (out *SignupResponse, err error) {
	if res.MsgType != SignupResponseType {
		return nil, fmt.Errorf(`3 Signup response type invalid, should be %d, was %d`, SignupResponseType, res.MsgType)
	}
	if res.Err != nil {
		return nil, errors.Join(errors.New("Error on signup response"), res.Err)
	}
	name := string(res.Bytes)
	if name != expName {
		return nil, fmt.Errorf("names did not match: %s %s", name, expName)
	}

	return &SignupResponse{ClientName: name}, nil
}

func (res ReceivedMsg) String() string {
	if res.Err != nil {
		return fmt.Sprintf("\nErroneous response: %s", res.Err.Error())
	}
	return res.SocketMsgUnsafe().String()
}
func (res ReceivedMsg) SocketMsg() (out *SocketMessage, err error) {
	if res.MsgType == MessageTypeError {
		if res.Err != nil {
			return nil, errors.Join(errors.New("Error found on incoming message"), res.Err)
		}
		return nil, errors.New("Erroneous message without error attached")
	}
	if res.Err != nil {
		return nil, errors.Join(errors.New("Error on signup response without appropriate message type"), res.Err)
	}
	return &SocketMessage{
		Type: res.MsgType,
		Data: res.Bytes,
	}, nil
}
func (res ReceivedMsg) SocketMsgUnsafe() (out *SocketMessage) {
	return &SocketMessage{
		Type: res.MsgType,
		Data: res.Bytes,
	}
}
func (res ReceivedMsg) AsSignupRequest() (out *SignupRequest, err error) {
	if res.Err != nil {
		return nil, errors.Join(errors.New("error on signup response"), res.Err)
	}
	if res.MsgType != SignupRequestType {
		return nil, fmt.Errorf(`signup request type invalid, should be %d, was %d`, SignupRequestType, res.MsgType)
	}
	if res.Bytes == nil {
		err = errors.New("signup request must not have nil bytes")
		return out, err
	} else {
		if len(res.Bytes) == 0 {
			err = errors.New("signup request must not have empty bytes")
			return out, err
		}
	}
	out, err = res.SocketMsgUnsafe().ParseSignupRequest()
	if err != nil {
		return nil, errors.Join(errors.New("failed to unmarshal signup request received"), err)
	}
	return out, nil
}

const RfidByteSize = 8

//func (res ReceivedMsg) ValidateWriteRequest() (toWrite [RfidByteSize]byte, err error) {
//	if res.Err != nil {
//		return [RfidByteSize]byte{}, res.Err
//	}
//	req, err := res.GetRequestData(websocket.BinaryMessage, FirstByteWrite)
//	if err != nil {
//		return [RfidByteSize]byte{}, err
//	}
//	if len(req) != RfidByteSize {
//		return [RfidByteSize]byte{}, errors.New("invalid write request size")
//	}
//	return [RfidByteSize]byte(req), nil
//}

func (res ReceivedMsg) ProcessWriteResponse(expectedWritten [RfidByteSize]byte) error {
	if res.Err != nil {
		return errors.New("error on write response: " + res.Err.Error())
	}
	if res.MsgType != WriteResponseType {
		return fmt.Errorf("invalid write response type. Expected %d, got %d", WriteResponseType, res.MsgType)
	}
	if len(res.Bytes) != RfidByteSize {
		return errors.New("invalid write response size")
	}
	if string(expectedWritten[:]) != string(res.Bytes) {
		return errors.New("bad response content, wrote wrong Bytes")
	}
	return nil
	//resp, err := res.GetRequestData(websocket.BinaryMessage, FirstByteRead)
	//if len(resp) != RfidByteSize {
	//	return out, errors.New("bad read response size")
	//}
	//return [RfidByteSize]byte(resp), nil

	//resp, err := res.GetRequestData(websocket.BinaryMessage, FirstByteWrite)
	//if err != nil {
	//	return err
	//}
	//if string(expectedWritten[:]) != string(resp) {
	//	return errors.New("bad response content, wrote wrong Bytes")
	//}
	//return nil
}

func (res ReceivedMsg) ProcessReadResponse() (bytesRead [RfidByteSize]byte, err error) {
	if res.Err != nil {
		println("error processing read response: " + res.Err.Error())
		return [RfidByteSize]byte{}, errors.New("error on read response: " + res.Err.Error())
	}
	if res.Bytes == nil {
		println("nil result bytes!: ")
		return [RfidByteSize]byte{}, errors.New("invalid read response size")
	}
	if len(res.Bytes) != RfidByteSize {
		println("bad byte size, result:" + string(res.Bytes))
		return [RfidByteSize]byte{}, errors.New("invalid read response size")
	}
	if res.MsgType != ReadResponseType {
		println("bad response type:", res.MsgType, "should be", ReadResponseType)
		return [RfidByteSize]byte{}, errors.New("invalid read response type")
	}
	return [RfidByteSize]byte(res.Bytes), nil
}
