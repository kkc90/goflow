package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
)

// CONSTANTS
const IN_BYTES = 1
const IN_PKTS = 2
const FLOWS = 3
const PROTOCOL = 4
const TOS = 5
const TCP_FLAGS = 6
const L4_SRC_PORT = 7
const IPV4_SRC_ADDR = 8
const SRC_MASK = 9
const INPUT_SNMP = 10
const L4_DST_PORT = 11
const IPV4_DST_ADDR = 12
const DST_MASK = 13
const OUTPUT_SNMP = 14
const IPV4_NEXT_HOP = 15

var FUNCTIONMAP = map[uint16]func([]byte) Value{
	IN_BYTES:      GetInt,
	PROTOCOL:      GetInt,
	L4_SRC_PORT:   GetInt,
	IPV4_SRC_ADDR: GetAddr,
}

//
// Value represents the interface to flowRecord Field values
// Field values can be of many types but should always implement the same methods
type Value interface {
	toString() string
}

//
// Integer Values
//
type IntValue struct {
	Data int
}

func (i IntValue) toString() string {
	return fmt.Sprintf("%v", i.Data)
}

//
// Address Values
//
type AddrValue struct {
	Data net.IP
}

func (a AddrValue) toString() string {
	return fmt.Sprintf("%v", a.Data.String())
}

//
// GENERICS
//
type netflow struct {
	Templates map[uint16]netflowPacketTemplate
}
type netflowPacket struct {
	Header    netflowPacketHeader
	Length    int
	Templates map[uint16]netflowPacketTemplate
	Data      []netflowDataFlowset
}
type netflowPacketHeader struct {
	Version  uint16
	Count    int16
	Uptime   int32
	Sequence int32
	Id       int32
}
type netflowPacketFlowset struct {
	FlowSetID uint16
	Length    uint16
}

// TEMPLATE STRUCTS
type netflowPacketTemplate struct {
	FlowSetID   uint16
	Length      uint16
	ID          uint16
	FieldCount  uint16
	Fields      []templateField
	FieldLength uint16
}
type templateField struct {
	FieldType uint16
	Length    uint16
}

// DATA STRUCTS
type netflowDataFlowset struct {
	FlowSetID uint16
	Length    uint16
	Records   []flowRecord
}
type flowRecord struct {
	Values []Value
}

func (r flowRecord) toString() string {
	var sl []string
	for _, v := range r.Values {
		sl = append(sl, v.toString())
	}
	return strings.Join(sl, " : ") + "\n"
}

// Retrieve an addr value from a field
func GetAddr(p []byte) Value {
	var a AddrValue
	var ip net.IP
	ip = p
	a.Data = ip
	return a
}

// Retrieve integer values from a field
func GetInt(p []byte) Value {
	var i IntValue
	switch {
	case len(p) > 2:
		i.Data = int(binary.BigEndian.Uint32(p))
		return i
	case len(p) > 1:
		i.Data = int(binary.BigEndian.Uint16(p))
		return i
	default:
		i.Data = int(uint8(p[0]))
		return i
	}
}

/*
ParseData

Takes a slice of a data flowset and retreives all the flow records
Requires
	p []byte : Data Flowset slice
*/
func parseData(n netflowPacket, p []byte) netflowDataFlowset {
	nfd := netflowDataFlowset{
		FlowSetID: binary.BigEndian.Uint16(p[:2]),
		Length:    binary.BigEndian.Uint16(p[2:4]),
	}

	// Return no flow records if it's empty
	if _, ok := n.Templates[nfd.FlowSetID]; !ok {
		return nfd
	}
	t := n.Templates[nfd.FlowSetID]

	start := uint16(4)
	// Read each Field in order from the flowset until the length is exceeded
	for start < nfd.Length {
		// Check the number of fields don't overrun the size of this flowset
		// if so, remainder must be padding
		if t.FieldLength <= (nfd.Length - start) {
			fr := flowRecord{}
			for _, f := range t.Fields {
				valueSlice := p[start : start+f.Length]
				if function, ok := FUNCTIONMAP[f.FieldType]; ok {
					value := function(valueSlice)
					fr.Values = append(fr.Values, value)
				}

				start = start + f.Length
			}
			nfd.Records = append(nfd.Records, fr)
			fmt.Printf(fr.toString())
		} else {
			fmt.Printf("Padding detected: %v\n", (nfd.Length-t.FieldLength)-4)
			start = start + (nfd.Length - t.FieldLength)
		}
	}
	return nfd
}

/*
ParseTemplate

Slices a flow template out of an overall packet
Requires
	p []byte : Full packet bytes
Returns
	netFlowPacketTemplate: Struct of template
*/
func parseTemplate(templateSlice []byte) netflowPacketTemplate {

	template := netflowPacketTemplate{
		Fields: make([]templateField, 0),
	}
	template.ID = binary.BigEndian.Uint16(templateSlice[4:6])

	// Get the number of Fields
	template.FieldCount = binary.BigEndian.Uint16(templateSlice[6:8])
	// Start at the first fields and work through
	fieldStart := 8
	var read = uint16(0)
	for read < template.FieldCount {
		fieldTypeEnd := fieldStart + 2
		fieldType := binary.BigEndian.Uint16(templateSlice[fieldStart:fieldTypeEnd])
		fieldLengthEnd := fieldTypeEnd + 2
		fieldLength := binary.BigEndian.Uint16(templateSlice[fieldTypeEnd:fieldLengthEnd])

		// Create template FIELD struct
		field := templateField{
			FieldType: fieldType,
			Length:    fieldLength,
		}
		// Template fields are IN ORDER
		// Order determines records in data flowset
		template.Fields = append(template.Fields, field)

		read++
		fieldStart = fieldLengthEnd
		template.FieldLength = template.FieldLength + fieldLength
	}
	fmt.Printf("%v\n", template.FieldLength)
	return template
}

/*
Route
Takes an entire packet slice, and routes each flowset to the correct handler

Requires
	netflowPacket : netflowpacket struct
	[]byte		  : Packet bytes
	uint16		  : Byte index to start at (skip the headers, etc)
*/
func Route(nfp netflowPacket, p []byte, start uint16) {
	id := uint16(0)
	l := uint16(0)

	//fmt.Printf("End of slice: %v", start+id.Length)
	for int(start) < nfp.Length {
		id = binary.BigEndian.Uint16(p[start : start+2])
		l = binary.BigEndian.Uint16(p[start+2 : start+4])
		// Slice the next flowset out
		s := p[start : start+l]
		// Flowset ID is the switch we use to determine what sort of flowset follors
		switch {
		// Template flowset
		case id == uint16(0):
			t := parseTemplate(s)
			nfp.Templates[t.ID] = t
		// Data flowset
		case id > uint16(255):
			d := parseData(nfp, s)
			nfp.Data = append(nfp.Data, d)
		}
		start = start + l
	}
}

func main() {

	// Provides parsing for Netflow V9 Records
	// https://www.ietf.org/rfc/rfc3954.txt

	addr := net.UDPAddr{
		Port: 9999,
		IP:   net.ParseIP("127.0.0.1"),
	}
	conn, err := net.ListenUDP("udp", &addr)

	if err != nil {
		fmt.Printf("Some error %v\n", err)
		return
	}

	nfpacket := netflowPacket{
		Templates: make(map[uint16]netflowPacketTemplate),
	}

	p := netflowPacketHeader{}
	// Buffer creates an array of bytes
	// We want to read the entire datagram in as UDP is type SOCK_DGRAM and "Read" can't be called more than once
	packet := make([]byte, 1500)
	// Read the max number of bytes in a datagram(1500) into a variable length slice of bytes, 'Buffer'
	// Also set the total number of bytes read so we can check it later
	nfpacket.Length, _ = conn.Read(packet)
	fmt.Printf("Total packet length: %v\n", nfpacket.Length)
	p.Version = binary.BigEndian.Uint16(packet[:2])
	switch p.Version {
	case 5:
		fmt.Printf("Wrong Netflow version, only v9 supported.")
		os.Exit(1)
	}
	nfpacket.Header = p

	Route(nfpacket, packet, uint16(20))
}
