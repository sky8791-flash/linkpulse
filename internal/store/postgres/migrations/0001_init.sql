CREATE TABLE links (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code VARCHAR(32) NOT NULL UNIQUE,
    target_url TEXT NOT NULL,
    expires_at TIMESTAMPTZ,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX links_created_at_id_idx
    ON links (created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE click_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    link_id BIGINT NOT NULL REFERENCES links(id),
    occurred_at TIMESTAMPTZ NOT NULL,
    visitor_hash CHAR(64) NOT NULL,
    referrer TEXT NOT NULL DEFAULT '',
    device VARCHAR(16) NOT NULL,
    browser VARCHAR(32) NOT NULL
);

CREATE INDEX click_events_link_time_idx
    ON click_events (link_id, occurred_at DESC);

CREATE INDEX click_events_link_referrer_idx
    ON click_events (link_id, referrer);
