package telemetry

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/protos"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// SizeLimit maximum incoming payload size from the vehicle
	SizeLimit = 1000000 // 1mb
	// https://github.com/protocolbuffers/protobuf-go/blob/6d0a5dbd95005b70501b4cc2c5124dab07a1f4a0/encoding/protojson/well_known_types.go#L591
	maxSecondsInDuration = 315576000000
	unknownFieldsHexPrefixLength = 64
	unknownFieldsNumbersLogLimit = 50
)

var (
	jsonOptions = protojson.MarshalOptions{
		UseEnumNumbers:  false,
		EmitUnpopulated: true,
		Indent:          ""}
	scientificNotationFloatRegex = regexp.MustCompile(`^[+-]?(\d*\.\d+|\d+\.\d*)([eE][+-]?\d+)$`)
)

// Record is a structs that represents the telemetry records vehicles send to the backend
// vin is used as kafka produce partitioning key by default, can be configured to random
type Record struct {
	ProduceTime            time.Time
	ReceivedTimestamp      int64
	Serializer             *BinarySerializer
	SocketID               string
	Timestamp              int64
	Txid                   string
	TxType                 string
	TripID                 string
	DeviceClientVersion    string
	Version                int
	Vin                    string
	PayloadBytes           []byte
	RawBytes               []byte
	transmitDecodedRecords bool
	protoMessage           proto.Message
}

// NewRecord Sanitizes and instantiates a Record from a message
// !! caller expect *Record to not be nil !!
func NewRecord(ts *BinarySerializer, msg []byte, socketID string, transmitDecodedRecords bool) (*Record, error) {
	if len(msg) > SizeLimit {
		return &Record{Serializer: ts, transmitDecodedRecords: transmitDecodedRecords}, ErrMessageTooBig
	}

	rec, err := ts.Deserialize(msg, socketID)
	rec.transmitDecodedRecords = transmitDecodedRecords
	if err != nil {
		return rec, err
	}
	err = rec.applyRecordTransforms()
	return rec, err
}

// Ack returns an ack response from the serializer
func (record *Record) Ack() []byte {
	return record.Serializer.Ack(record)
}

// Error returns an error response from the serializer
func (record *Record) Error(err error) []byte {
	return record.Serializer.Error(err, record)
}

// Metadata converts record to metadata map
func (record *Record) Metadata() map[string]string {
	metadata := make(map[string]string)
	metadata["vin"] = record.Vin
	metadata["receivedat"] = fmt.Sprint(record.ReceivedTimestamp)
	metadata["timestamp"] = fmt.Sprint(record.Timestamp)
	metadata["txid"] = record.Txid
	metadata["txtype"] = record.TxType
	metadata["version"] = fmt.Sprint(record.Version)
	metadata["device_client_version"] = record.DeviceClientVersion
	return metadata
}

// Payload returns the bytes of the telemetry record gdata
func (record *Record) Payload() []byte {
	return record.PayloadBytes
}

// GetJSONPayload marshals to JSON if requested
func (record *Record) GetJSONPayload() ([]byte, error) {
	if record.transmitDecodedRecords {
		return record.Payload(), nil
	}
	return record.toJSON()
}

// Raw returns the raw telemetry record
func (record *Record) Raw() []byte {
	return record.RawBytes
}

// LengthRawBytes gets the record's flatbuffer payload byte size
func (record *Record) LengthRawBytes() int {
	record.ensureEncoded()
	return len(record.RawBytes)
}

// Length gets the records byte size
func (record *Record) Length() int {
	return len(record.PayloadBytes)
}

// Encode encodes the records into bytes
func (record *Record) Encode() ([]byte, error) {
	record.ensureEncoded()
	return record.RawBytes, nil
}

// Dispatch uses the configuration to send records to the list of backends/data stores they belong
func (record *Record) Dispatch() {
	logger := record.Serializer.Logger()
	logger.Log(logrus.DEBUG, "dispatching_message", logrus.LogInfo{"socket_id": record.SocketID, "payload": record.Raw()})
	record.Serializer.Dispatch(record)
}

func (record *Record) ensureEncoded() {
	if record.RawBytes == nil && record.Serializer != nil && record.Serializer.Logger() != nil {
		record.Serializer.Logger().ErrorLog("record_RawBytes_blank", nil, nil)
	}
}

// SignalsCount received per record_type from the vehicle
func (record *Record) SignalsCount() int {
	switch payload := record.protoMessage.(type) {
	case *protos.Payload:
		return len(payload.GetData())
	case *protos.VehicleAlerts:
		return len(payload.GetAlerts())
	default:
		return 0
	}
}

func (record *Record) applyProtoRecordTransforms() error {
	switch record.TxType {
	case "alerts":
		message := &protos.VehicleAlerts{}
		err := proto.Unmarshal(record.Payload(), message)
		if err != nil {
			return err
		}
		record.logUnknownProtoFields(message)
		message.Vin = record.Vin
		transformTimestamp(message)
		record.PayloadBytes, err = proto.Marshal(message)
		record.protoMessage = message
		return err
	case "errors":
		message := &protos.VehicleErrors{}
		err := proto.Unmarshal(record.Payload(), message)
		if err != nil {
			return err
		}
		record.logUnknownProtoFields(message)
		message.Vin = record.Vin
		record.PayloadBytes, err = proto.Marshal(message)
		record.protoMessage = message
		return err
	case "V":
		message := &protos.Payload{}
		err := proto.Unmarshal(record.Payload(), message)
		if err != nil {
			return err
		}
		record.logUnknownProtoFields(message)
		record.logUnknownPayloadFieldKeys(message)
		message.Vin = record.Vin
		transformLocation(message)
		transformScientificNotation(message)
		record.PayloadBytes, err = proto.Marshal(message)
		record.protoMessage = message
		return err
	case "connectivity":
		message := &protos.VehicleConnectivity{}
		err := proto.Unmarshal(record.Payload(), message)
		if err != nil {
			return err
		}
		record.logUnknownProtoFields(message)
		message.Vin = record.Vin
		record.PayloadBytes, err = proto.Marshal(message)
		record.protoMessage = message
		return err
	default:
		return nil
	}
}

func (record *Record) applyRecordTransforms() error {
	var err error
	if err = record.applyProtoRecordTransforms(); err != nil {
		return err
	}
	if !record.transmitDecodedRecords {
		return nil
	}
	record.PayloadBytes, err = record.toJSON()
	return err
}

// GetProtoMessage gets extracted protobuf message
func (record *Record) GetProtoMessage() proto.Message {
	return record.protoMessage
}

// ToJSON serializes the record to a JSON data in bytes
func (record *Record) toJSON() ([]byte, error) {
	return jsonOptions.Marshal(record.protoMessage)
}

// transformLocation does a best-effort attempt to convert the Location field to a proper protos.Location
// type if what we receive is a string that can be parsed. This should make the transition from strings to
// Locations easier to handle downstream.
func transformLocation(message *protos.Payload) {
	for _, datum := range message.Data {
		if datum.GetKey() == protos.Field_Location {
			if strVal := datum.GetValue().GetStringValue(); strVal != "" {
				if loc, err := ParseLocation(strVal); err == nil {
					datum.Value = &protos.Value{Value: &protos.Value_LocationValue{LocationValue: loc}}
				}
			}
			// There can be only one Field_Location Datum in the proto; abort once we've seen it.
			return
		}
	}
}

// transformScientificNotation fixes floating point values which are represented in scientific notation
// example: 1e-3 => 0.001
func transformScientificNotation(message *protos.Payload) {
	for _, datum := range message.Data {
		if strVal := datum.GetValue().GetStringValue(); strVal != "" {
			if scientificNotationFloatRegex.MatchString(strVal) {
				if floatVal, err := strconv.ParseFloat(strVal, 32); err == nil {
					datum.Value = &protos.Value{Value: &protos.Value_StringValue{StringValue: fmt.Sprintf("%.5f", floatVal)}}
				}
			}
		}
	}
}

// ParseLocation parses a location string (such as "(37.412374 N, 122.145867 W)") into a *proto.Location type.
func ParseLocation(s string) (*protos.LocationValue, error) {
	var lat, lon float64
	var latQ, lonQ string
	count, err := fmt.Sscanf(s, "(%f %1s, %f %1s)", &lat, &latQ, &lon, &lonQ)
	if err != nil {
		return nil, err
	}
	if count != 4 || !strings.Contains("NS", latQ) || !strings.Contains("EW", lonQ) {
		return nil, fmt.Errorf("invalid location format: %s", s)
	}
	if latQ == "S" {
		lat = -lat
	}
	if lonQ == "W" {
		lon = -lon
	}
	return &protos.LocationValue{
		Latitude:  lat,
		Longitude: lon,
	}, nil
}

func transformTimestamp(message *protos.VehicleAlerts) {
	for _, alert := range message.Alerts {
		alert.StartedAt = convertTimestamp(alert.StartedAt)
		alert.EndedAt = convertTimestamp(alert.EndedAt)
	}
}

func convertTimestamp(input *timestamppb.Timestamp) *timestamppb.Timestamp {
	if input == nil {
		return nil
	}
	if input.GetSeconds() < maxSecondsInDuration {
		return input
	}
	return timestamppb.New(time.UnixMilli(input.GetSeconds()))
}

func (record *Record) logUnknownProtoFields(message proto.Message) {
	logger := record.Serializer.Logger()
	if logger == nil || message == nil {
		return
	}

	unknown := message.ProtoReflect().GetUnknown()
	if len(unknown) == 0 {
		return
	}

	fieldNumbers, truncated, parseErr := decodeUnknownFieldNumbers(unknown, unknownFieldsNumbersLogLimit)
	prefixLength := len(unknown)
	if prefixLength > unknownFieldsHexPrefixLength {
		prefixLength = unknownFieldsHexPrefixLength
	}

	logInfo := logrus.LogInfo{
		"vin":                      record.Vin,
		"txid":                     record.Txid,
		"record_type":              record.TxType,
		"payload_size_bytes":       len(record.Payload()),
		"unknown_fields_size_bytes": len(unknown),
		"unknown_field_numbers":    fieldNumbers,
		"unknown_fields_truncated": truncated,
		"unknown_fields_hex_prefix": hex.EncodeToString(unknown[:prefixLength]),
	}
	if parseErr != "" {
		logInfo["unknown_fields_parse_error"] = parseErr
	}

	logger.Log(logrus.WARN, "unknown_proto_fields_detected", logInfo)
}

func decodeUnknownFieldNumbers(unknown []byte, maxFields int) ([]int32, bool, string) {
	if maxFields <= 0 {
		return nil, false, ""
	}

	fieldMap := make(map[int32]struct{})
	b := unknown

	for len(b) > 0 {
		number, fieldType, tagLength := protowire.ConsumeTag(b)
		if tagLength < 0 {
			return sortedFieldNumbers(fieldMap), false, protowire.ParseError(tagLength).Error()
		}

		b = b[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, fieldType, b)
		if valueLength < 0 {
			return sortedFieldNumbers(fieldMap), false, protowire.ParseError(valueLength).Error()
		}

		b = b[valueLength:]
		if _, ok := fieldMap[int32(number)]; !ok {
			fieldMap[int32(number)] = struct{}{}
			if len(fieldMap) >= maxFields {
				return sortedFieldNumbers(fieldMap), len(b) > 0, ""
			}
		}
	}

	return sortedFieldNumbers(fieldMap), false, ""
}

func sortedFieldNumbers(fieldMap map[int32]struct{}) []int32 {
	if len(fieldMap) == 0 {
		return nil
	}

	fields := make([]int32, 0, len(fieldMap))
	for field := range fieldMap {
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool {
		return fields[i] < fields[j]
	})

	return fields
}

func (record *Record) logUnknownPayloadFieldKeys(payload *protos.Payload) {
	logger := record.Serializer.Logger()
	if logger == nil || payload == nil {
		return
	}

	unknownFieldKeyMap := make(map[int32]struct{})
	for _, datum := range payload.GetData() {
		if datum == nil {
			continue
		}
		fieldKey := int32(datum.GetKey())
		if _, isKnown := protos.Field_name[fieldKey]; !isKnown {
			unknownFieldKeyMap[fieldKey] = struct{}{}
		}
	}

	unknownFieldKeys := sortedFieldNumbers(unknownFieldKeyMap)
	if len(unknownFieldKeys) == 0 {
		return
	}

	logger.Log(logrus.WARN, "unknown_payload_field_keys_detected", logrus.LogInfo{
		"vin":                       record.Vin,
		"txid":                      record.Txid,
		"record_type":               record.TxType,
		"unknown_payload_field_keys": unknownFieldKeys,
	})
}
