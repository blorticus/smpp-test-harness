package smppth

import (
	"fmt"
	"net"
	"smpp"
)

// SMSC represents an SMPP 3.4 server, which accepts one or more transport connections and responds
// to bind requests
type SMSC struct {
	name                                        string
	ip                                          net.IP
	port                                        uint16
	mapOfConnectionTowardRemotePeersByTheirName map[string]net.Conn
	assertedSystemID                            string
}

// NewSMSC creates a new SMSC agent.
func NewSMSC(smscName string, smscBindSystemID string, listeningIP net.IP, listeningPort uint16) *SMSC {
	if smscBindSystemID == "" {
		smscBindSystemID = smscName
	}

	return &SMSC{name: smscName, ip: listeningIP, port: listeningPort, mapOfConnectionTowardRemotePeersByTheirName: make(map[string]net.Conn), assertedSystemID: smscBindSystemID}
}

// Name returns the name of this SMSC agent instance
func (smsc *SMSC) Name() string {
	return smsc.name
}

// SendMessageToPeer instructs this SMSC agent to send a message to the peer identified in the
// MessageDescriptor.  No effort is made to validate that the MessageDescriptor SourceAgentName
// matches this agent's name.
func (smsc *SMSC) SendMessageToPeer(message *MessageDescriptor) error {
	connectionTowardNamedPeer := smsc.mapOfConnectionTowardRemotePeersByTheirName[message.NameOfRemotePeer]

	if connectionTowardNamedPeer == nil {
		return fmt.Errorf("This Agent is not bound to a peer named (%s)", message.NameOfRemotePeer)
	}

	encodedPDU, err := message.PDU.Encode()
	smsc.panicIfError(err)

	_, err = connectionTowardNamedPeer.Write(encodedPDU)
	smsc.panicIfError(err)

	return nil
}

// StartEventLoop instructs this SMSC agent to start listening for incoming transport connections,
// to respond to binds, to emit AgentEvents to the agentEventChannel, and accept
// messages for remote delivery via SendMessageToPeer().
func (smsc *SMSC) StartEventLoop(agentEventChannel chan<- *AgentEvent) {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", smsc.ip.String(), smsc.port))
	smsc.panicIfError(err)

	for {
		incomingTransport, err := listener.Accept()
		smsc.panicIfError(err)

		peerMessageHandler := newSmscPeerMessageHandler(smsc, incomingTransport)
		go peerMessageHandler.startHandlingPeerConnection(agentEventChannel)
	}
}

func (smsc *SMSC) notifySmscOfConnectionToWhichNamePeerIsBound(peerNameAssertedInBindRequest string, connectionTowardPeer net.Conn) {
	smsc.mapOfConnectionTowardRemotePeersByTheirName[peerNameAssertedInBindRequest] = connectionTowardPeer
}

func (smsc *SMSC) panicIfError(err error) {
	if err != nil {
		panic(err)
	}
}

type smscPeerMessageHandler struct {
	connectionToPeer net.Conn
	streamReader     *smpp.NetworkStreamReader
	parentSMSC       *SMSC
	nameOfRemotePeer string
}

func newSmscPeerMessageHandler(parentSmsc *SMSC, transportConnectionToPeer net.Conn) *smscPeerMessageHandler {
	return &smscPeerMessageHandler{
		connectionToPeer: transportConnectionToPeer,
		streamReader:     smpp.NewNetworkStreamReader(transportConnectionToPeer),
		parentSMSC:       parentSmsc,
	}
}

func (handler *smscPeerMessageHandler) startHandlingPeerConnection(agentEventChannel chan<- *AgentEvent) {
	pdus, err := handler.streamReader.ExtractNextPDUs()
	handler.parentSMSC.panicIfError(err)

	if pdus[0].CommandID != smpp.CommandBindTransceiver {
		handler.parentSMSC.panicIfError(fmt.Errorf("First PDU from peer (%s) should be bind-transceiver, but was (%s)", handler.connectionToPeer.RemoteAddr().String(), pdus[0].CommandName()))
	}

	handler.nameOfRemotePeer = handler.extractPeerNameFromTransceiverBind(pdus[0])
	agentEventChannel <- &AgentEvent{RemotePeerName: handler.nameOfRemotePeer, SourceAgent: handler.parentSMSC, Type: ReceivedBind, SmppPDU: pdus[0]}

	bindResponsePDU := handler.sendTransceiverResponseToPeerBasedOnRequestBind(pdus[0])
	handler.parentSMSC.notifySmscOfConnectionToWhichNamePeerIsBound(handler.nameOfRemotePeer, handler.connectionToPeer)

	agentEventChannel <- &AgentEvent{RemotePeerName: handler.nameOfRemotePeer, SourceAgent: handler.parentSMSC, Type: CompletedBind, SmppPDU: bindResponsePDU}

	for i := 1; i < len(pdus); i++ {
		agentEventChannel <- &AgentEvent{Type: ReceivedMessage, SmppPDU: pdus[i], RemotePeerName: handler.nameOfRemotePeer, SourceAgent: handler.parentSMSC}
	}

	for {
		pdus, err := handler.streamReader.ExtractNextPDUs()
		handler.parentSMSC.panicIfError(err)

		for _, pdu := range pdus {
			agentEventChannel <- &AgentEvent{Type: ReceivedMessage, SmppPDU: pdu, RemotePeerName: handler.nameOfRemotePeer, SourceAgent: handler.parentSMSC}
		}
	}
}

func (handler *smscPeerMessageHandler) extractPeerNameFromTransceiverBind(pdu *smpp.PDU) string {
	return pdu.MandatoryParameters[0].Value.(string)
}

func (handler *smscPeerMessageHandler) sendTransceiverResponseToPeerBasedOnRequestBind(bindTransceiverPdu *smpp.PDU) (bindResponsePDU *smpp.PDU) {
	smscName := handler.makeNameShortEnoughForSmppSystemIDField(handler.parentSMSC.Name())

	bindResponsePDU = smpp.NewPDU(smpp.CommandBindTransceiverResp, 0, bindTransceiverPdu.SequenceNumber, []*smpp.Parameter{
		smpp.NewCOctetStringParameter(smscName),
	}, []*smpp.Parameter{})

	encodedBindResponse, _ := bindResponsePDU.Encode()

	_, err := handler.connectionToPeer.Write(encodedBindResponse)
	handler.parentSMSC.panicIfError(err)

	return bindResponsePDU
}

func (handler *smscPeerMessageHandler) makeNameShortEnoughForSmppSystemIDField(name string) string {
	if len(name) > 16 {
		return name[:16]
	}

	return name
}