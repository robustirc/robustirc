package outputstream

import (
	"encoding/binary"
	"unsafe"
)

// To avoid additional dependencies on libraries like flatbuffers or capnproto
// and yet achieve high encoding/decoding speed and low memory usage, these are
// hand-written functions to marshal/unmarshal messageBatches.

func (m *messageBatch) marshal() []byte {
	bufLen := unsafe.Sizeof(uint64(0)) /* NextID */ +
		unsafe.Sizeof(uint64(0)) /* len(Messages) */
	for _, msg := range m.Messages {
		bufLen += 2*unsafe.Sizeof(uint64(0)) /* RobustId */ +
			unsafe.Sizeof(uint64(0)) /* len(Data) */ +
			unsafe.Sizeof(byte(0))*uintptr(len(msg.Data)) /* Data */ +
			unsafe.Sizeof(uint64(0)) /* len(InterestingFor) */ +
			unsafe.Sizeof(uint64(0))*uintptr(len(msg.InterestingFor)) /* InterestingFor */
	}

	buffer := make([]byte, bufLen)
	n := 0
	binary.LittleEndian.PutUint64(buffer[n:], m.NextID)
	n += 8
	binary.LittleEndian.PutUint64(buffer[n:], uint64(len(m.Messages)))
	n += 8
	for _, msg := range m.Messages {
		binary.LittleEndian.PutUint64(buffer[n:], uint64(msg.Id.Id))
		n += 8
		binary.LittleEndian.PutUint64(buffer[n:], uint64(msg.Id.Reply))
		n += 8
		binary.LittleEndian.PutUint64(buffer[n:], uint64(len(msg.Data)))
		n += 8
		copy(buffer[n:], msg.Data)
		n += len(msg.Data)
		binary.LittleEndian.PutUint64(buffer[n:], uint64(len(msg.InterestingFor)))
		n += 8
		for session := range msg.InterestingFor {
			binary.LittleEndian.PutUint64(buffer[n:], uint64(session))
			n += 8
		}
	}
	return buffer
}

func unmarshalMessageBatch(buffer []byte) *messageBatch {
	var result messageBatch
	var lenData int
	var lenInterestingFor uint64
	n := 0
	result.NextID = binary.LittleEndian.Uint64(buffer[n:])
	n += 8
	result.Messages = make([]Message, binary.LittleEndian.Uint64(buffer[n:]))
	n += 8
	for i := 0; i < len(result.Messages); i++ {
		msg := &(result.Messages[i])
		msg.Id.Id = binary.LittleEndian.Uint64(buffer[n:])
		n += 8
		msg.Id.Reply = binary.LittleEndian.Uint64(buffer[n:])
		n += 8
		lenData = int(binary.LittleEndian.Uint64(buffer[n:]))
		n += 8
		msg.Data = string(buffer[n : n+lenData])
		n += lenData
		lenInterestingFor = binary.LittleEndian.Uint64(buffer[n:])
		msg.InterestingFor = make(map[uint64]bool, lenInterestingFor)
		n += 8
		for j := uint64(0); j < lenInterestingFor; j++ {
			msg.InterestingFor[uint64(binary.LittleEndian.Uint64(buffer[n:]))] = true
			n += 8
		}
	}
	return &result
}
