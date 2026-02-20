package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

type OrderRequest struct {
	Amount float64 `json:"amount"`
	UserID string  `json:"user_id"`
}

type OrderResponse struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

var tracer trace.Tracer

func initTracer() func() {
	jaegerEndpoint := os.Getenv("JAEGER_ENDPOINT")
	if jaegerEndpoint == "" {
		jaegerEndpoint = "http://jaeger-collector:14268/api/traces"
	}

	exp, err := jaeger.New(jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(jaegerEndpoint)))
	if err != nil {
		log.Fatal(err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("service-b"),
			semconv.ServiceVersion("v1.0.0"),
		)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	tracer = otel.Tracer("service-b")

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tp.Shutdown(ctx)
	}
}

func createOrderHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	span.SetAttributes(
		attribute.Float64("order.amount", req.Amount),
		attribute.String("order.user_id", req.UserID),
	)

	orderID := generateOrderID(ctx)

	time.Sleep(100 * time.Millisecond)

	response := OrderResponse{
		OrderID: orderID,
		Status:  "created",
	}

	span.SetAttributes(
		attribute.String("order.id", orderID),
		attribute.String("order.status", "created"),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func generateOrderID(ctx context.Context) string {
	_, span := tracer.Start(ctx, "generate-order-id")
	defer span.End()

	orderID := fmt.Sprintf("order-%d", time.Now().Unix())
	span.SetAttributes(attribute.String("generated.order_id", orderID))

	return orderID
}

func main() {
	shutdown := initTracer()
	defer shutdown()

	mux := http.NewServeMux()
	mux.HandleFunc("/orders", createOrderHandler)

	handler := otelhttp.NewHandler(mux, "service-b")

	log.Println("Orders service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}
