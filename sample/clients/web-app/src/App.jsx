import { useEffect, useState } from 'react';
import { listProducts, getCart, addToCart, removeFromCart, checkout } from './api.js';

function formatCents(cents) {
  return `$${(cents / 100).toFixed(2)}`;
}

export default function App() {
  // 1 and 2 are seeded by baseline (seeds/users.sql: ada@example.com,
  // grace@example.com) — any other id 404s at checkout with "unknown user".
  const [userId, setUserId] = useState('1');
  const [products, setProducts] = useState([]);
  const [cartItems, setCartItems] = useState([]);
  const [error, setError] = useState(null);
  const [order, setOrder] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listProducts().then(setProducts).catch((err) => setError(err.message));
  }, []);

  useEffect(() => {
    refreshCart();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [userId]);

  function refreshCart() {
    if (!userId) return;
    getCart(userId)
      .then((cart) => setCartItems(cart.items || []))
      .catch((err) => setError(err.message));
  }

  async function run(action) {
    setError(null);
    setBusy(true);
    try {
      await action();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  const productById = new Map(products.map((p) => [p.id, p]));
  const cartTotal = cartItems.reduce((sum, item) => {
    const product = productById.get(item.product_id);
    return sum + (product ? product.price_cents * item.quantity : 0);
  }, 0);

  return (
    <div className="page">
      <header>
        <h1>brew</h1>
        <label>
          user id
          <input value={userId} onChange={(e) => setUserId(e.target.value)} />
        </label>
      </header>

      {error && <p className="error">{error}</p>}

      <section>
        <h2>menu</h2>
        <ul className="products">
          {products.map((product) => (
            <li key={product.id}>
              <span>{product.name}</span>
              <span>{formatCents(product.price_cents)}</span>
              <button
                disabled={busy}
                onClick={() => run(async () => {
                  await addToCart(userId, product.id, 1);
                  refreshCart();
                })}
              >
                add to cart
              </button>
            </li>
          ))}
        </ul>
      </section>

      <section>
        <h2>cart</h2>
        {cartItems.length === 0 && <p>empty</p>}
        <ul className="cart">
          {cartItems.map((item) => {
            const product = productById.get(item.product_id);
            return (
              <li key={item.product_id}>
                <span>{product ? product.name : `product ${item.product_id}`}</span>
                <span>x{item.quantity}</span>
                <button
                  disabled={busy}
                  onClick={() => run(async () => {
                    await removeFromCart(userId, item.product_id);
                    refreshCart();
                  })}
                >
                  remove
                </button>
              </li>
            );
          })}
        </ul>
        {cartItems.length > 0 && (
          <>
            <p className="total">total: {formatCents(cartTotal)}</p>
            <button
              disabled={busy}
              onClick={() => run(async () => {
                setOrder(null);
                const result = await checkout(userId);
                setOrder(result);
                refreshCart();
              })}
            >
              checkout
            </button>
          </>
        )}
      </section>

      {order && (
        <section>
          <h2>order confirmed</h2>
          <p>
            order #{order.id} — {order.status} — {formatCents(order.total_cents)}
          </p>
        </section>
      )}
    </div>
  );
}
