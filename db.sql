CREATE TABLE users (
    id uuid PRIMARY KEY,
    xivauthid uuid NOT NULL,
    username TEXT DEFAULT NULL,
    verified_characters BOOLEAN NOT NULL,
    banned BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE characters_claim (
    id TEXT PRIMARY KEY,/* XIVAuth persistent key*/
    claim_by uuid REFERENCES users(id),
    lodestone_id INTEGER NOT NULL,
    avatar_url TEXT,
    portrait_url TEXT,
    claim_registered TIMESTAMPTZ NOT NULL DEFAULT now()/*When the user registered on loggingway*/
);


CREATE TABLE characters (
    id BIGINT PRIMARY KEY,/*ContentID of PC*/
    xivauthkey TEXT DEFAULT NULL REFERENCES characters_claim(id),
    charname TEXT NOT NULL,
    datacenter SMALLINT NOT NULL,
    homeworld SMALLINT NOT NULL
);

/*
CREATE TABLE reports (
    id BIGSERIAL PRIMARY KEY,
    created_by uuid REFERENCES users(id),
    report_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
*/

CREATE TABLE encounters(
    id BIGSERIAL PRIMARY KEY,
    zone_id INTEGER,
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    uploaded_by uuid REFERENCES users(id),
    payload BYTEA NOT NULL
);

CREATE TABLE encounter_player_stats (
    id BIGSERIAL PRIMARY KEY,
    encounter_id BIGSERIAL NOT NULL REFERENCES encounters(id) ON DELETE CASCADE,
    uploaded_by uuid REFERENCES users(id),
    player_id BIGINT NOT NULL,/*ID here is relative to the payload,aka gameobjectid of the player in the report*/
	player_name TEXT NOT NULL,
    total_damage BIGINT NOT NULL,
    total_pscore BIGINT NOT NULL,
    total_healing BIGINT NOT NULL,

    total_hits BIGINT NOT NULL,
    total_crits BIGINT NOT NULL,
    total_direct_hits BIGINT NOT NULL,

    first_timestamp BIGINT NOT NULL,
    last_timestamp BIGINT NOT NULL,
    duration_seconds DOUBLE PRECISION NOT NULL,

    dps DOUBLE PRECISION NOT NULL,
    hps DOUBLE PRECISION NOT NULL,
    crit_rate DOUBLE PRECISION NOT NULL,
    direct_hit_rate DOUBLE PRECISION NOT NULL,

    created_at TIMESTAMPTZ DEFAULT now(),

    UNIQUE(encounter_id, player_id)
);

CREATE TABLE encounter_player_action_stats (
    id BIGSERIAL PRIMARY KEY,
    encounter_id BIGSERIAL NOT NULL REFERENCES encounters(id) ON DELETE CASCADE,

    player_id BIGINT NOT NULL,
    action_id INTEGER NOT NULL,

    total_damage BIGINT NOT NULL,
    hits BIGINT NOT NULL,
    crits BIGINT NOT NULL,
    direct_hits BIGINT NOT NULL,

    crit_rate DOUBLE PRECISION NOT NULL,
    direct_hit_rate DOUBLE PRECISION NOT NULL,

    created_at TIMESTAMPTZ DEFAULT now(),

    UNIQUE(encounter_id, player_id, action_id)
);
