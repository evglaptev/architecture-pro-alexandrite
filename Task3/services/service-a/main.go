package main

import (
	"bytes"
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

type CalculationRequest struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
}

type CalculationResponse struct {
	Result  float64 `json:"result"`
	OrderID string  `json:"order_id"`
}

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
			semconv.ServiceName("service-a"),
			semconv.ServiceVersion("v1.0.0"),
		)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	tracer = otel.Tracer("service-a")

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tp.Shutdown(ctx)
	}
}

func calculateHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	var req CalculationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	span.SetAttributes(
		attribute.Float64("calculation.a", req.A),
		attribute.Float64("calculation.b", req.B),
	)

	result := req.A + req.B
	span.SetAttributes(attribute.Float64("calculation.result", result))

	orderID, err := createOrder(ctx, result)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "Failed to create order", http.StatusInternalServerError)
		return
	}

	response := CalculationResponse{
		Result:  result,
		OrderID: orderID,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func createOrder(ctx context.Context, amount float64) (string, error) {
	ctx, span := tracer.Start(ctx, "create-order")
	defer span.End()

	ordersServiceURL := os.Getenv("ORDERS_SERVICE_URL")
	if ordersServiceURL == "" {
		ordersServiceURL = "http://service-b:8080"
	}

	orderReq := OrderRequest{
		Amount: amount,
		UserID: "user123",
	}

	reqBody, err := json.Marshal(orderReq)
	if err != nil {
		span.RecordError(err)
		return "", err
	}

	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "POST", ordersServiceURL+"/orders",
		bytes.NewBuffer(reqBody))
	if err != nil {
		span.RecordError(err)
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	span.SetAttributes(
		attribute.String("http.method", "POST"),
		attribute.String("http.url", ordersServiceURL+"/orders"),
		attribute.Float64("order.amount", amount),
	)

	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		return "", err
	}
	defer resp.Body.Close()

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("orders service returned status %d", resp.StatusCode)
		span.RecordError(err)
		return "", err
	}

	var orderResp OrderResponse
	if err := json.NewDecoder(resp.Body).Decode(&orderResp); err != nil {
		span.RecordError(err)
		return "", err
	}

	span.SetAttributes(attribute.String("order.id", orderResp.OrderID))
	return orderResp.OrderID, nil
}

func main() {
	shutdown := initTracer()
	defer shutdown()

	mux := http.NewServeMux()
	mux.HandleFunc("/calculate", calculateHandler)

	handler := otelhttp.NewHandler(mux, "service-a")

	log.Println("Calculation service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}
