ALTER TABLE delivery_attempts
    DROP CONSTRAINT delivery_attempts_delivery_id_attempt_number_key;

CREATE INDEX delivery_attempts_timeline_idx
    ON delivery_attempts (delivery_id, started_at DESC);

