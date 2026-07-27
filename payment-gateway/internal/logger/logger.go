package logger

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// Configure configura o logger global da aplicação
func Configure() {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	addSource := strings.EqualFold(os.Getenv("LOG_SOURCE"), "true")

	// Opções com formatação consistente
	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: addSource,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Normalizar timestamps para formato ISO
			if a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					a.Value = slog.StringValue(t.Format(time.RFC3339Nano))
				}
			}
			return a
		},
	}

	// Suporte a formato texto para desenvolvimento
	var handler slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))
}

// parseLogLevel converte string para nível de log
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// WithContext adiciona campos comuns a todos os logs
func WithContext(attrs ...slog.Attr) *slog.Logger {
	return slog.Default().With(attrsToAny(attrs)...)
}

// WithRequestID adiciona request ID ao logger
func WithRequestID(requestID string) *slog.Logger {
	return slog.Default().With(slog.String("request_id", requestID))
}

// WithOrderID adiciona order ID ao logger
func WithOrderID(orderID string) *slog.Logger {
	return slog.Default().With(slog.String("order_id", orderID))
}

// WithUserID adiciona user ID ao logger
func WithUserID(userID string) *slog.Logger {
	return slog.Default().With(slog.String("user_id", userID))
}

// LogError loga erro com stack trace (opcional)
func LogError(msg string, err error, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("error", err.Error()))
	slog.Error(msg, attrsToAny(attrs)...)
}

func attrsToAny(attrs []slog.Attr) []any {
	args := make([]any, len(attrs))
	for i, attr := range attrs {
		args[i] = attr
	}
	return args
}
