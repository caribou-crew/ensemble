package com.brew.order;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.servlet.http.HttpServletRequest;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

@RestController
@RequestMapping("/orders")
public class OrderController {

    private final OrderRepository repo;
    private final StringRedisTemplate redis;
    private final HttpClient http = HttpClient.newHttpClient();
    private final ObjectMapper mapper = new ObjectMapper();

    @Value("${catalog.url}")
    private String catalogUrl;
    @Value("${user.url}")
    private String userUrl;
    @Value("${payments.url}")
    private String paymentsUrl;

    public OrderController(OrderRepository repo, StringRedisTemplate redis) {
        this.repo = repo;
        this.redis = redis;
    }

    @GetMapping
    public List<Order> list() {
        return repo.findAll();
    }

    @GetMapping("/{id}")
    public ResponseEntity<Order> get(@PathVariable Long id) {
        return repo.findById(id).map(ResponseEntity::ok).orElse(ResponseEntity.notFound().build());
    }

    @PostMapping
    public ResponseEntity<?> create(@RequestBody CreateOrderRequest in, HttpServletRequest req) throws Exception {
        HttpResponse<String> userResp = http.send(
                TraceHeaders.forward(HttpRequest.newBuilder(URI.create(userUrl + "/users/" + in.getUserId())), req)
                        .GET().build(),
                HttpResponse.BodyHandlers.ofString());
        if (userResp.statusCode() != 200) {
            return ResponseEntity.badRequest().body(Map.of("error", "unknown user " + in.getUserId()));
        }

        Order order = new Order();
        order.setUserId(in.getUserId());
        List<OrderItem> items = new ArrayList<>();
        long total = 0;

        for (CreateOrderRequest.Line line : in.getItems()) {
            HttpResponse<String> productResp = http.send(
                    TraceHeaders.forward(HttpRequest.newBuilder(URI.create(catalogUrl + "/products/" + line.getProductId())), req)
                            .GET().build(),
                    HttpResponse.BodyHandlers.ofString());
            if (productResp.statusCode() != 200) {
                return ResponseEntity.badRequest().body(Map.of("error", "unknown product " + line.getProductId()));
            }
            JsonNode product = mapper.readTree(productResp.body());
            long unitPrice = product.get("price_cents").asLong();

            OrderItem item = new OrderItem();
            item.setOrder(order);
            item.setProductId(line.getProductId());
            item.setQuantity(line.getQuantity());
            item.setUnitPriceCents(unitPrice);
            items.add(item);

            total += unitPrice * line.getQuantity();
        }
        order.setItems(items);
        order.setTotalCents(total);

        String chargeBody = mapper.writeValueAsString(Map.of("amount_cents", total, "user_id", in.getUserId()));
        HttpResponse<String> chargeResp = http.send(
                TraceHeaders.forward(HttpRequest.newBuilder(URI.create(paymentsUrl + "/charges")), req)
                        .header("content-type", "application/json")
                        .POST(HttpRequest.BodyPublishers.ofString(chargeBody))
                        .build(),
                HttpResponse.BodyHandlers.ofString());
        order.setStatus(chargeResp.statusCode() / 100 == 2 ? "paid" : "payment_failed");

        Order saved = repo.save(order);

        if ("paid".equals(saved.getStatus())) {
            String notification = mapper.writeValueAsString(Map.of(
                    "order_id", saved.getId(),
                    "user_id", saved.getUserId(),
                    "total_cents", saved.getTotalCents()));
            redis.opsForList().leftPush("orders:notify", notification);
        }

        return ResponseEntity.status(201).body(saved);
    }
}
