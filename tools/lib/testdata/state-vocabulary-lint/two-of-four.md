# Below the threshold

A session occupies a concurrency slot while `working` or `waiting`.

That is a deliberate partition, not a stale enumeration — it is the single
most common two-state spelling in the repo (session.HasWorkInFlight names
seven sites). The gate's threshold of three exists so this line does NOT
fire; at a threshold of two roughly half of all hits are lines like it.
