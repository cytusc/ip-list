-- Read-only range audit: rank enabled ranges by recent output so dead ranges can
-- be retired. Run against the production database; it changes nothing.
--
--   sudo -u postgres psql -d ipguard -f scripts/range-audit.sql
--
-- Columns:
--   hits_14d     successful IPs still available inside this range in the last 14 days
--   scanned_14d  distinct addresses inspected in the last 14 days
--   hit_rate     hits_14d / scanned_14d, rounded to four decimals
--   verdict      retire when 0 hits over a real sample; watch when the rate is tiny

WITH ranges AS (
 SELECT n.id, n.name, n.cidr, masklen(n.cidr) AS prefix_len
 FROM networks n
 WHERE n.enabled
), pool AS (
 SELECT p.ip, p.available, p.checked_at
 FROM node_ip_pool p
 WHERE p.checked_at > now() - interval '14 days'
)
SELECT r.name,
       r.cidr::text AS cidr,
       r.prefix_len,
       count(p.ip) FILTER (WHERE p.available) AS hits_14d,
       count(p.ip) AS scanned_14d,
       round((count(p.ip) FILTER (WHERE p.available))::numeric / NULLIF(count(p.ip), 0), 4) AS hit_rate,
       CASE
        WHEN count(p.ip) = 0 THEN 'never scanned'
        WHEN count(p.ip) FILTER (WHERE p.available) = 0 THEN 'retire'
        WHEN count(p.ip) FILTER (WHERE p.available)::numeric / count(p.ip) < 0.001 THEN 'watch'
        ELSE 'keep'
       END AS verdict
FROM ranges r
LEFT JOIN pool p ON p.ip <<= r.cidr
GROUP BY r.id, r.name, r.cidr, r.prefix_len
ORDER BY hits_14d ASC, scanned_14d DESC, r.name
LIMIT 200;
