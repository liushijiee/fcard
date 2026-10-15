package epusdt

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
)

type NotificationRecorder interface {
	Record(context.Context, Notification) error
}

func NewCallbackHandler(verifier *CallbackVerifier, recorder NotificationRecorder) (http.Handler, error) {
	if verifier == nil || recorder == nil {
		return nil, errors.New("callback verifier and durable recorder are required")
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" || request.Header.Get("Content-Encoding") != "" {
			http.Error(writer, "unsupported notification format", http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxNotificationBytes))
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				http.Error(writer, "notification too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(writer, "invalid notification", http.StatusBadRequest)
			}
			return
		}
		notification, err := verifier.Verify(body)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrNotificationSignature) {
				status = http.StatusUnauthorized
			}
			http.Error(writer, "invalid notification", status)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
		defer cancel()
		if recorder.Record(ctx, notification) != nil {
			http.Error(writer, "notification storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		io.WriteString(writer, "ok")
	}), nil
}
