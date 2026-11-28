---
name: binance
description: Read the user's Binance account (balances, earn, futures positions, open orders, trades, deposits) and, only when the user asks, place or cancel a spot, USDⓈ-M or COIN-M limit or market order (or simulate one in paper trading mode) after phone confirmation. Use when the user asks about Binance, crypto holdings, futures positions or wants to buy, sell, simulate or cancel on Binance.
---

# Binance

Read tools (`binance_overview`, `binance_balances`, `binance_positions`, `binance_open_orders`, `binance_order`, `binance_trades`, `binance_income`, `binance_transfers`, `binance_price`, `binance_limits`, `binance_status`) never change anything.

Live writes (`binance_spot_order`, `binance_spot_cancel`, `binance_usdm_order`, `binance_usdm_cancel`, `binance_coinm_order`, `binance_coinm_cancel`) are always confirmed on the user's phone (`confirm_always`).

When `binance_limits` reports `paper_trading: true`, only the simulated order actions (`binance_paper_spot_order`, `binance_paper_usdm_order`, `binance_paper_coinm_order`) are advertised. Use the matching `binance_paper_*` action, still confirmed on the phone, and clearly describe its result as simulated (no exchange order is placed; there are no resting simulated orders or paper cancellations).

## Before an order

1. Only place an order the user asked for in this conversation, with the symbol, side and size they gave. Never decide to buy or sell for them, and never suggest an order as investment advice.
2. Call `binance_price` and `binance_limits` first. Tell the user the current mode (live or paper), current price, estimated amount in USDT and how much of today's allowance is left.
3. A limit order needs `price`; a market buy on spot can use `quote_amount` (USDT to spend) instead of `quantity`.
4. For futures in hedge mode pass `position_side` (`LONG` / `SHORT`); `reduce_only` is for one-way mode only.

## When a guard refuses

The extension refuses orders outside the whitelist, above the per-order or daily limit, above the maximum leverage, with a limit price too far from the market, or a market order whose price moved too much since the card. Tell the user exactly what was refused and why. Do not split an order, change the symbol or move the price to get around a guard, and do not ask to change the guard settings yourself: the user changes them in Settings › Extensions › 币安.

## Never

- There are no withdrawal, transfer, loan, leverage or margin-mode tools. Say so if asked; do not look for another way.
- Do not post balances, positions or order amounts to group chats.
- Do not ask the user for API keys or private keys in the conversation; they enter them only in Settings › Extensions › 币安.
