CREATE INDEX IF NOT EXISTS idx_kyc_requests_user_created_at
	ON kyc_requests(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_swaps_user_created_at
	ON swaps(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_dca_strategies_user_created_at
	ON dca_strategies(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_webhook_subscriptions_user_created_at
	ON webhook_subscriptions(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_assets_active_symbol
	ON assets(active, symbol);

CREATE INDEX IF NOT EXISTS idx_countries_active_code
	ON countries(active, code);

CREATE INDEX IF NOT EXISTS idx_payment_rails_country_active_id
	ON payment_rails(country_code, active, id);
