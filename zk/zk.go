package zk

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/canhlinh/gozk"
)

type AttendanceRecord struct {
	UserID    int
	Timestamp time.Time
}

type ZKDevice struct {
	IP   string
	Port int
}

type ZKManager struct {
	IP         string
	Port       int
	zkTimezone string
}

func NewZKManager(ip string, port string) (*ZKManager, error) {
	intPort, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}
	return &ZKManager{
		IP:         ip,
		Port:       intPort,
		zkTimezone: "Asia/Dhaka",
	}, nil
}

func (zk *ZKManager) GetAttendance(since time.Time) ([]AttendanceRecord, error) {
	socket := gozk.NewZK("", zk.IP, zk.Port, 0, zk.zkTimezone)
	// Psocket := NewZK("", testZkHost, testZkPort, 0, testTimezone)
	err := socket.Connect()
	if condition := err != nil; condition {
		log.Printf("Error connecting to ZK device: %v", err)
		return nil, fmt.Errorf("connection error: %w", err)

	}
	socket.DisableDevice()
	defer socket.Disconnect()
	defer socket.EnableDevice()
	attendances, err := socket.GetAllScannedEvents()
	if err != nil {
		return nil, fmt.Errorf("failed to get attendance: %w", err)
	}
	if len(attendances) == 0 {
		return nil, fmt.Errorf("no attendance records found")
	}

	// log.Printf("Attendance records: %v", attendances)
	// for _, attendance := range attendances {
	// 	log.Printf("Attendance User: %d", attendance.UserID)
	// 	log.Printf("Attendance Timestamp: %s", attendance.Timestamp)
	// }

	records := make([]AttendanceRecord, 0)
	for _, attendance := range attendances {
		if attendance.Timestamp.After(since) {
			record := AttendanceRecord{
				UserID:    int(attendance.UserID),
				Timestamp: attendance.Timestamp,
			}
			records = append(records, record)
		}
	}
	// log.Printf("Filtered Attendance records: %v", records)
	// time.Sleep(time.Second * 1)
	return records, nil
}

func VerifyProof(proof []byte) bool {
	// Zero-knowledge proof verification logic
	return false
}
