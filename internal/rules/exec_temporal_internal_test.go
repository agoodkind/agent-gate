package rules

import (
	"testing"
	"unsafe"
)

func TestTemporalRecordMarshalRejectsOverflowingValue(t *testing.T) {
	var valueByte byte
	maxInt := int(^uint(0) >> 1)
	overflowingLength := maxInt - temporalRecordHeaderBytes + 1
	overflowingValue := unsafe.String(&valueByte, overflowingLength)

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("MarshalBinary panicked: %v", recovered)
		}
	}()

	_, err := (temporalRecord{
		receiptID: 1,
		available: true,
		value:     overflowingValue,
	}).MarshalBinary()
	if err == nil {
		t.Fatal("MarshalBinary error = nil, want oversized record error")
	}
}

func TestTemporalRecordMarshalRoundTripsValue(t *testing.T) {
	record, err := (temporalRecord{
		receiptID: 42,
		available: true,
		value:     "response",
	}).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	receiptID, available, value, valid := decodeTemporalRecord(record)
	if !valid || receiptID != 42 || !available || value != "response" {
		t.Fatalf(
			"decodeTemporalRecord = (%d, %v, %q, %v), want (42, true, response, true)",
			receiptID,
			available,
			value,
			valid,
		)
	}
}
