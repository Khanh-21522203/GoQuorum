package observability

// LogLevel represents log severity.
//
// (v1: internal/observability/logger.go LogLevel)
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

// String returns the human-readable name of the level.
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

// ParseLogLevel parses a string into a LogLevel, defaulting to LevelInfo
// for unrecognized input.
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

// LogEntry is a single structured log record.
//
// (v1: internal/observability/logger.go LogEntry)
type LogEntry struct {
	Timestamp string
	Level     string
	Logger    string
	Message   string
	Fields    map[string]interface{}
}

// LoggerConfig configures a Logger.
//
// (v1: internal/observability/logger.go LoggerConfig)
type LoggerConfig struct {
	Level  string
	Format string // "json" or "text"
	Output string // "stdout" or a file path
	NodeID string
}

// Logger is a structured logger that tags every entry with a name and an
// optional node ID.
//
// (v1: internal/observability/logger.go Logger)
type Logger struct {
	name   string
	level  LogLevel
	format string
	nodeID string
}

// NewLogger creates a new named logger.
//
// TODO(v2): import os; open cfg.Output (defaulting to os.Stdout when unset
// or "stdout") and store it as the write target (v1:
// internal/observability/logger.go NewLogger).
func NewLogger(name string, cfg LoggerConfig) *Logger {
	return &Logger{name: name, level: ParseLogLevel(cfg.Level), format: cfg.Format, nodeID: cfg.NodeID}
}

// WithName returns a copy of the logger under a different name.
func (l *Logger) WithName(name string) *Logger {
	return &Logger{name: name, level: l.level, format: l.format, nodeID: l.nodeID}
}

// Debug logs a debug-level message.
//
// TODO(v2): import encoding/json, time; build a LogEntry, tag it with
// node_id if set, and write it to the configured output in json or text
// format, gated on l.level (v1: internal/observability/logger.go
// Logger.log/Debug).
func (l *Logger) Debug(msg string, fields ...map[string]interface{}) {
}

// Info logs an info-level message.
//
// TODO(v2): see Debug (v1: internal/observability/logger.go Logger.Info).
func (l *Logger) Info(msg string, fields ...map[string]interface{}) {
}

// Warn logs a warn-level message.
//
// TODO(v2): see Debug (v1: internal/observability/logger.go Logger.Warn).
func (l *Logger) Warn(msg string, fields ...map[string]interface{}) {
}

// Error logs an error-level message.
//
// TODO(v2): see Debug (v1: internal/observability/logger.go Logger.Error).
func (l *Logger) Error(msg string, fields ...map[string]interface{}) {
}

// Fatal logs a fatal-level message and terminates the process.
//
// TODO(v2): see Debug, then os.Exit(1) (v1:
// internal/observability/logger.go Logger.Fatal).
func (l *Logger) Fatal(msg string, fields ...map[string]interface{}) {
}

// F builds a fields map from alternating key/value pairs, for use with the
// Logger methods.
//
// (v1: internal/observability/logger.go F)
func F(kv ...interface{}) map[string]interface{} {
	fields := make(map[string]interface{})
	for i := 0; i < len(kv)-1; i += 2 {
		if key, ok := kv[i].(string); ok {
			fields[key] = kv[i+1]
		}
	}
	return fields
}
