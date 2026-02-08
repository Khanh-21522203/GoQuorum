package observability

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// LogLevel represents log severity
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

func (l LogLevel) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	case LevelFatal:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

// ParseLogLevel parses a string to LogLevel
func ParseLogLevel(s string) LogLevel {
	switch s {
	case "DEBUG", "debug":
		return LevelDebug
	case "INFO", "info":
		return LevelInfo
	case "WARN", "warn", "WARNING", "warning":
		return LevelWarn
	case "ERROR", "error":
		return LevelError
	case "FATAL", "fatal":
		return LevelFatal
	default:
		return LevelInfo
	}
}

// LogEntry represents a structured log entry
type LogEntry struct {
	Timestamp string                 `json:"timestamp"`
	Level     string                 `json:"level"`
	Logger    string                 `json:"logger"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// Logger is a structured logger
type Logger struct {
	mu        sync.Mutex
	name      string
	level     LogLevel
	output    io.Writer
	format    string // "json" or "text"
	nodeID    string
}

// LoggerConfig holds logger configuration
type LoggerConfig struct {
	Level  string
	Format string
	Output string
	NodeID string
}

// NewLogger creates a new logger
func NewLogger(name string, cfg LoggerConfig) *Logger {
	var output io.Writer = os.Stdout
	if cfg.Output != "" && cfg.Output != "stdout" {
		f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			output = f
		}
	}

	format := "json"
	if cfg.Format != "" {
		format = cfg.Format
	}

	return &Logger{
		name:   name,
		level:  ParseLogLevel(cfg.Level),
		output: output,
		format: format,
		nodeID: cfg.NodeID,
	}
}

// WithName returns a new logger with the given name
func (l *Logger) WithName(name string) *Logger {
	return &Logger{
		name:   name,
		level:  l.level,
		output: l.output,
		format: l.format,
		nodeID: l.nodeID,
	}
}

// log writes a log entry
func (l *Logger) log(level LogLevel, msg string, fields map[string]interface{}) {
	if level < l.level {
		return
	}

	entry := LogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level.String(),
		Logger:    l.name,
		Message:   msg,
		Fields:    fields,
	}

	if l.nodeID != "" {
		if entry.Fields == nil {
			entry.Fields = make(map[string]interface{})
		}
		entry.Fields["node_id"] = l.nodeID
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.format == "json" {
		data, _ := json.Marshal(entry)
		fmt.Fprintln(l.output, string(data))
	} else {
		// Text format
		fieldsStr := ""
		if len(entry.Fields) > 0 {
			data, _ := json.Marshal(entry.Fields)
			fieldsStr = " " + string(data)
		}
		fmt.Fprintf(l.output, "%s [%s] %s: %s%s\n",
			entry.Timestamp, entry.Level, entry.Logger, entry.Message, fieldsStr)
	}
}

// Debug logs a debug message
func (l *Logger) Debug(msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(LevelDebug, msg, f)
}

// Info logs an info message
func (l *Logger) Info(msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(LevelInfo, msg, f)
}

// Warn logs a warning message
func (l *Logger) Warn(msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(LevelWarn, msg, f)
}

// Error logs an error message
func (l *Logger) Error(msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(LevelError, msg, f)
}

// Fatal logs a fatal message and exits
func (l *Logger) Fatal(msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(LevelFatal, msg, f)
	os.Exit(1)
}

// Field helpers for common fields

// F creates a fields map from key-value pairs
func F(kv ...interface{}) map[string]interface{} {
	fields := make(map[string]interface{})
	for i := 0; i < len(kv)-1; i += 2 {
		if key, ok := kv[i].(string); ok {
			fields[key] = kv[i+1]
		}
	}
	return fields
}

// Global logger instance
var defaultLogger = NewLogger("goquorum", LoggerConfig{Level: "INFO", Format: "json"})

// SetDefaultLogger sets the default logger
func SetDefaultLogger(l *Logger) {
	defaultLogger = l
}

// GetLogger returns a named logger
func GetLogger(name string) *Logger {
	return defaultLogger.WithName(name)
}

// Package-level convenience functions

func Debug(msg string, fields ...map[string]interface{}) {
	defaultLogger.Debug(msg, fields...)
}

func Info(msg string, fields ...map[string]interface{}) {
	defaultLogger.Info(msg, fields...)
}

func Warn(msg string, fields ...map[string]interface{}) {
	defaultLogger.Warn(msg, fields...)
}

func Error(msg string, fields ...map[string]interface{}) {
	defaultLogger.Error(msg, fields...)
}

func Fatal(msg string, fields ...map[string]interface{}) {
	defaultLogger.Fatal(msg, fields...)
}
