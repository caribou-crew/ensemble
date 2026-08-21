package com.brew.order;

import java.util.List;

public class CreateOrderRequest {
    private Long userId;
    private List<Line> items;

    public Long getUserId() { return userId; }
    public void setUserId(Long userId) { this.userId = userId; }
    public List<Line> getItems() { return items; }
    public void setItems(List<Line> items) { this.items = items; }

    public static class Line {
        private Long productId;
        private Integer quantity;

        public Long getProductId() { return productId; }
        public void setProductId(Long productId) { this.productId = productId; }
        public Integer getQuantity() { return quantity; }
        public void setQuantity(Integer quantity) { this.quantity = quantity; }
    }
}
