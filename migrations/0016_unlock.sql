-- `musdash unlock <email>` cannot reach the attempt counts, which are in the
-- server's memory. It writes a time here instead, and the server, about to
-- refuse the account for too many attempts, forgets the counts if that time
-- has not passed. 0 when nobody asked.
ALTER TABLE users ADD COLUMN unlock_until INTEGER NOT NULL DEFAULT 0;
