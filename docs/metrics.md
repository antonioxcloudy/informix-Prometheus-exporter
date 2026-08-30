# Catálogo de métricas

Todas las métricas de Informix incluyen la etiqueta `informixserver`. Los ratios de ocupación se publican como fracciones entre `0` y `1`; los dashboards los convierten a porcentaje cuando corresponde. Los contadores terminados en `_total` deben consultarse normalmente con `rate()` o `increase()`.

Prometheus obtiene rendimiento, actividad e I/O de chunks desde `/metrics/fast` cada minuto. `/metrics/storage` publica capacidad de dbspaces y límites de tablas desde una caché: consulta Informix al arrancar y refresca el snapshot cada dos horas. `/metrics` sigue exponiendo el catálogo completo. Aunque las métricas de I/O se documentan junto a dbspaces y chunks, pertenecen deliberadamente al grupo rápido.

## Estado del exporter

- `informix_up`: resultado del ping ODBC por instancia.
- `informix_exporter_build_info`: versión, commit y fecha de compilación.
- `informix_exporter_scrape_success`: vale `1` únicamente cuando todas las instancias terminan correctamente.
- `informix_exporter_scrape_duration_seconds`: duración total del scrape concurrente.
- `informix_exporter_scrape_errors_total`: errores acumulados por instancia.
- `informix_exporter_last_successful_scrape_timestamp_seconds`: último scrape global correcto.
- `informix_exporter_instance_scrape_success`: resultado individual de cada instancia.
- `informix_exporter_instance_scrape_duration_seconds`: duración individual por instancia.
- `informix_exporter_instance_last_successful_scrape_timestamp_seconds`: último éxito individual.
- `informix_exporter_collector_success`: resultado por instancia y collector.
- `informix_exporter_collector_duration_seconds`: duración por instancia y collector.

## Rendimiento y perfil

- `informix_buffer_operations_total`: lecturas, escrituras y esperas de buffer, además de páginas leídas y escritas en disco, separadas por tamaño de página.
- `informix_buffer_turnover_ratio`: BTR por tamaño de página. Representa las rotaciones medias del buffer pool por hora desde el último reinicio de las estadísticas; no es un porcentaje.
- `informix_isam_operations_total`: operaciones ISAM acumuladas por tipo.
- `informix_page_reads_total`: páginas leídas acumuladas.
- `informix_read_ahead_pages_total`: páginas de read-ahead por tipo.
- `informix_sequential_scans_total`: escaneos secuenciales acumulados.
- `informix_physical_log_writes_total`: escrituras acumuladas del physical log.
- `informix_waits_total`: esperas internas por tipo.
- `informix_lock_requests_total`: solicitudes de lock acumuladas.
- `informix_network_connections_total`: conexiones de red acumuladas.
- `informix_sorts_total`: ordenaciones totales, en memoria y en disco.
- `informix_checkpoint_duration_seconds`: duración del checkpoint más reciente.
- `informix_checkpoint_dirty_buffers`: buffers sucios tratados en el último checkpoint.
- `informix_checkpoint_flushes_per_second`: ritmo de flush del último checkpoint.
- `informix_last_backup_timestamp_seconds`: fecha del último backup de nivel 0.
- `informix_hdr_last_update_timestamp_seconds`: última actualización HDR registrada.

## Sesiones, locks, transacciones y logical logs

- `informix_sessions`: sesiones de usuario actuales.
- `informix_session_waiting`: sesiones esperando, separadas por motivo.
- `informix_oldest_session_age_seconds`: edad de la sesión más antigua.
- `informix_locks`: locks activos o con espera.
- `informix_locks_by_type`: locks activos por tipo Informix.
- `informix_open_transactions`: transacciones abiertas.
- `informix_long_transactions`: transacciones marcadas como largas.
- `informix_transaction_locks`: locks mantenidos por transacciones abiertas.
- `informix_logical_log_files`: número de logical logs configurados.
- `informix_logical_logs_not_backed_up`: logical logs usados pendientes de backup.
- `informix_logical_log_size_bytes`: tamaño de cada logical log.
- `informix_logical_log_used_bytes`: espacio usado de cada logical log.
- `informix_logical_log_used_ratio`: fracción usada de cada logical log.
- `informix_logical_log_current`: identifica el logical log actual.
- `informix_logical_log_backed_up`: indica si el logical log tiene backup.
- `informix_logical_log_archived`: indica si el logical log está archivado.
- `informix_logical_log_temporary`: identifica logical logs temporales.

## Dbspaces y chunks

- `informix_dbspace_size_bytes`, `informix_dbspace_used_bytes` e `informix_dbspace_free_bytes`: capacidad por dbspace.
- `informix_dbspace_used_ratio`: fracción ocupada por dbspace.
- `informix_dbspace_page_size_bytes`: tamaño de página del dbspace.
- `informix_dbspace_chunks`: número de chunks del dbspace.
- `informix_chunk_size_bytes`, `informix_chunk_used_bytes` e `informix_chunk_free_bytes`: capacidad por chunk.
- `informix_chunk_used_ratio`: fracción ocupada por chunk.
- `informix_chunk_online`: estado disponible y consistente del chunk.
- `informix_chunk_recovering`: indica si el chunk se está recuperando.
- `informix_chunk_extendable`: indica si el chunk es ampliable.
- `informix_chunk_io_operations_total`: operaciones físicas de lectura y escritura por chunk.
- `informix_chunk_io_pages_total`: páginas transferidas por chunk.

## Tablas y particiones (collector opcional y aparcado)

- `informix_table_extents`: extents totales sumados entre las particiones de la tabla.
- `informix_table_allocated_bytes` e `informix_table_used_bytes`: espacio agregado por tabla.
- `informix_partition_allocated_pages` e `informix_partition_used_pages`: páginas por partición.
- `informix_partition_page_size_bytes`: tamaño de página de la partición.
- `informix_partition_classic_page_limit`: límite clásico configurado para comparación.
- `informix_partition_classic_page_limit_ratio`: proximidad al límite clásico de páginas/ROWID.

## Métricas personalizadas

Cada entrada válida de `custom` genera `informix_custom_<nombre>`. Solo se admiten consultas escalares de una fila y una columna numérica para mantener controlada la cardinalidad.
