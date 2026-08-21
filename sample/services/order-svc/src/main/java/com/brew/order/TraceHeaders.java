package com.brew.order;

import jakarta.servlet.http.HttpServletRequest;
import java.net.http.HttpRequest;

/**
 * The whole propagation contract (same as catalog-svc's forwardTraceHeaders
 * and edge-gw's doc comment): carry traceparent/baggage from the inbound
 * request onto every outbound call order-svc makes on its own behalf.
 */
final class TraceHeaders {
    private TraceHeaders() {}

    static HttpRequest.Builder forward(HttpRequest.Builder builder, HttpServletRequest inbound) {
        String traceparent = inbound.getHeader("traceparent");
        if (traceparent != null) {
            builder.header("traceparent", traceparent);
        }
        String baggage = inbound.getHeader("baggage");
        if (baggage != null) {
            builder.header("baggage", baggage);
        }
        return builder;
    }
}
