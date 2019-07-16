package smppth

import (
	"fmt"
	"net"
	"smpp"
	"strconv"
	"strings"
	"testing"
)

func TestEsmePeerMessageListener(t *testing.T) {
	esme := &ESME{}

	conn := newFakeNetConn()

	connector := newEsmePeerMessageListener("testSmsc01", esme, conn)
	connector.streamReader = smpp.NewNetworkStreamReader(conn)

	conn.nextReadValue = testSmppMsgTransceiverResp01()
	connector.completeTransceiverBindingTowardPeer("esme01", "system", "password")

	pdu, err := smpp.DecodePDU(conn.lastWriteValue)

	if err != nil {
		t.Errorf("completeTransceiverBindingTowardPeer() should have returned tranceiver_bind_resp, but Decode() on conn Write() generated error = (%s)", err)
	}

	if pdu.CommandID != 0x00000009 {
		t.Errorf("completeTransceiverBindingTowardPeer() should have Write()n bind-tranceiver, but message type = (%s)", pdu.CommandName())
	}

	eventMsgChannel := make(chan *AgentEvent)

	conn.nextReadValue = testSmppMsgEnquireLink01()
	go connector.startListeningForIncomingMessagesFromPeer(eventMsgChannel)

	eventMessage := <-eventMsgChannel

	validationError := validateEventMessage(eventMessage, ReceivedMessage, "testSmsc01")

	if validationError != nil {
		t.Errorf("On first enquire_link from peer, for received event message, %s", validationError)
	}

	if eventMessage.SmppPDU == nil {
		t.Errorf("On first enquire_link from peer, for received event message, SmppPDU should not be nil, but is")
	}

	if eventMessage.SmppPDU.CommandID != smpp.CommandEnquireLink {
		t.Errorf("On first enquire_link from peer, for received event message, SmppPDU CommandID should be enquire_link, but is (%s)", eventMessage.SmppPDU.CommandName())
	}
}

func TestEsmeOneSmscEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	defer listener.Close()

	if err != nil {
		panic(fmt.Sprintf("Failed to create local listener for SMSC: %s", err))
	}

	portAsUint64, _ := strconv.ParseUint(strings.Split(listener.Addr().String(), ":")[1], 10, 16)
	smscListeningPort := uint16(portAsUint64)

	go smscSimulatedListener(listener)

	esme := NewEsme("testEsme01", net.ParseIP("127.0.0.1"), 0)
	esme.peerBinds = []smppBindInfo{
		smppBindInfo{
			remoteIP:   net.ParseIP("127.0.0.1"),
			remotePort: smscListeningPort,
			smscName:   "testSmsc01",
			password:   "password",
			systemID:   "esme01",
			systemType: "generic",
		},
	}

	esmeEventChannel := make(chan *AgentEvent)

	go esme.StartEventLoop(esmeEventChannel)

	nextEvent := <-esmeEventChannel

	if nextEvent.Type != CompletedBind {
		t.Errorf("For first received event, expected CompletedBind (%d), got (%d)", int(CompletedBind), int(nextEvent.Type))
	}

	esme.SendMessageToPeer(&MessageDescriptor{NameOfSourcePeer: "testEsme01", NameOfRemotePeer: "testSmsc01", PDU: testSmppPDUEnquireLink01()})

	nextEvent = <-esmeEventChannel

	if nextEvent.Type != ReceivedMessage {
		t.Errorf("For second received event, expected ReceivedMessage (%d), got (%d)", int(ReceivedMessage), int(nextEvent.Type))
	}

	if nextEvent.RemotePeerName != "testSmsc01" {
		t.Errorf("For second received event, expected nameOfMessageSender = (testSmsc01), got = (%s)", nextEvent.RemotePeerName)
	}

	if nextEvent.SmppPDU == nil {
		t.Errorf("For second received event, expect smppPDU not nil, but it is")
	} else {
		if nextEvent.SmppPDU.CommandID != smpp.CommandEnquireLinkResp {
			t.Errorf("For second received event, expect enquire-link-response, but got (%s)", nextEvent.SmppPDU.CommandName())
		}
	}
}

func smscSimulatedListener(listener net.Listener) {
	conn, err := listener.Accept()
	defer conn.Close()

	if err != nil {
		panic(fmt.Sprintf("Failed on simulated SMSC listener Accept(): %s", err))
	}

	lastReceivedPDU, err := simulatedSmscReceivePDUWithExpectations(conn, smpp.CommandBindTransceiver)

	if err != nil {
		panic(fmt.Sprintf("On wait for bind-transceiver from esme: %s", err))
	}

	bindRespPDU := smpp.NewPDU(smpp.CommandBindTransceiverResp, 0, lastReceivedPDU.SequenceNumber, []*smpp.Parameter{
		smpp.NewCOctetStringParameter("smsc01"),
	}, []*smpp.Parameter{})

	encodedPDU, _ := bindRespPDU.Encode()
	_, err = conn.Write(encodedPDU)

	if err != nil {
		panic(fmt.Sprintf("Failed on SMSC Write() of transceiver_bind_resp: %s", err))
	}

	lastReceivedPDU, err = simulatedSmscReceivePDUWithExpectations(conn, smpp.CommandEnquireLink)

	if err != nil {
		panic(fmt.Sprintf("On wait for first enquire-link: %s", err))
	}

	enquireLinkRespPDU := smpp.NewPDU(smpp.CommandEnquireLinkResp, 0, lastReceivedPDU.SequenceNumber, []*smpp.Parameter{}, []*smpp.Parameter{})

	encodedPDU, _ = enquireLinkRespPDU.Encode()
	_, err = conn.Write(encodedPDU)

	if err != nil {
		panic(fmt.Sprintf("Failed on SMSC Write() of enquire-link-resp: %s", err))
	}
}

func simulatedSmscReceivePDUWithExpectations(conn net.Conn, expectedCommandID smpp.CommandIDType) (*smpp.PDU, error) {
	readBuf := make([]byte, 65536)

	bytesRead, err := conn.Read(readBuf)

	if err != nil {
		return nil, fmt.Errorf("Failed on simulated SMSC listener Read(): %s", err)
	}

	readBuf = readBuf[:bytesRead]

	pdu, err := smpp.DecodePDU(readBuf)

	if err != nil {
		return nil, fmt.Errorf("Failed to decode initial PDU from peer: %s", err)
	}

	if pdu.CommandID != expectedCommandID {
		return pdu, fmt.Errorf("Received PDU from ESME.  Expect = (%s), got = (%s)", smpp.CommandName(expectedCommandID), pdu.CommandName())
	}

	return pdu, nil
}