-- Fix overly broad legacy channel backfill.
-- user_id is not a reliable channel signal: authenticated web rows can have user_id too.
-- Keep mobile only when an event explicitly marked the trade as mobile.

UPDATE orders o
   SET channel = 'web'
 WHERE COALESCE(NULLIF(o.channel, ''), 'web') = 'mobile'
   AND NOT EXISTS (
     SELECT 1
       FROM order_events e
      WHERE e.order_id = o.id
        AND (
          lower(COALESCE(e.payload ->> 'channel', '')) = 'mobile'
          OR lower(COALESCE(e.payload ->> 'surface', '')) = 'mobile'
        )
   );

UPDATE buy_orders bo
   SET channel = 'web'
 WHERE COALESCE(NULLIF(bo.channel, ''), 'web') = 'mobile'
   AND NOT EXISTS (
     SELECT 1
       FROM buy_order_events e
      WHERE e.buy_order_id = bo.id
        AND (
          lower(COALESCE(e.payload ->> 'channel', '')) = 'mobile'
          OR lower(COALESCE(e.payload ->> 'surface', '')) = 'mobile'
        )
   );

UPDATE orders SET channel = 'web' WHERE channel IS NULL OR channel = '';
UPDATE buy_orders SET channel = 'web' WHERE channel IS NULL OR channel = '';
