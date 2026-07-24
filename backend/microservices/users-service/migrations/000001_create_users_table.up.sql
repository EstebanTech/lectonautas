CREATE TABLE IF NOT EXISTS users (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email        VARCHAR(255) NOT NULL UNIQUE,
    username     VARCHAR(30)  NOT NULL UNIQUE,
    password     VARCHAR(255) NOT NULL,
    display_name VARCHAR(100),
    avatar_url   VARCHAR(500),
    bio          TEXT,
    is_active    BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
