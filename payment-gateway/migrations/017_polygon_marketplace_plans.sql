-- Enable Polygon USDT payment options for marketplace/capability plans.
INSERT INTO marketplace_plans (id, product_id, slug, name, price_amount, payment_asset, network, take_rate_bps, quota, validity_seconds, status)
VALUES
  ('plan_fx_400_polygon', 'prod_fx_enterprise', 'enterprise-400-polygon', 'Enterprise Pack Polygon', 400.000000, 'USDT', 'POLYGON', 2000, 100000, 2592000, 'active'),
  ('plan_ocr_80_polygon', 'prod_ocr_enterprise', 'enterprise-80-polygon', 'Enterprise Pack Polygon', 80.000000, 'USDT', 'POLYGON', 2000, 1000, 2592000, 'active'),
  ('plan_aml_600_polygon', 'prod_aml_screening', 'enterprise-600-polygon', 'Enterprise Pack Polygon', 600.000000, 'USDT', 'POLYGON', 2000, 10000, 2592000, 'active'),
  ('plan_gpt_300_polygon', 'prod_gpt_business', 'business-300-polygon', 'Business Credits Polygon', 300.000000, 'USDT', 'POLYGON', 2000, 100000, 2592000, 'active')
ON CONFLICT (id) DO NOTHING;
