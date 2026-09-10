//! When a workflow runs: a cron expression, or one instant.
//!
//! Cron expressions keep the meaning they have in a crontab. Loom accepts the
//! five-field form, the six-field form that starts with seconds, and the
//! `@daily` style macros.
//!
//! Times are UTC unless a schedule names a fixed offset. Loom does not read
//! the time zone database, so a schedule cannot follow a daylight saving rule.

mod cron_schedule;
mod error;
mod instant;
mod offset;
mod schedule;

pub use cron_schedule::CronSchedule;
pub use error::ScheduleError;
pub use instant::{format_instant, parse_instant};
pub use offset::UtcOffset;
pub use schedule::Schedule;
