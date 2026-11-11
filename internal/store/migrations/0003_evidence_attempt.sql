-- Agent investigations retry as a new attempt. Evidence remains linked to the
-- task, and this column keeps observations from different attempts distinct.
ALTER TABLE evidence ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;
