//! When a job runs: a cron expression, or one instant.

use std::fmt;
use std::time::SystemTime;

use crate::cron_schedule::CronSchedule;
use crate::instant::format_instant;

/// When a job runs.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Schedule {
    /// Runs every time the expression matches.
    Cron(CronSchedule),
    /// Runs once, at one instant.
    Once(SystemTime),
}

impl Schedule {
    /// The first time this schedule fires after `time`.
    ///
    /// A one-time schedule that already fired returns nothing, and so does a
    /// cron expression that has no match left, such as one with a past year.
    #[must_use]
    pub fn next_after(&self, time: SystemTime) -> Option<SystemTime> {
        match self {
            Self::Cron(schedule) => schedule.next_after(time),
            Self::Once(instant) => (*instant > time).then_some(*instant),
        }
    }

    /// True when the schedule can fire more than once.
    #[must_use]
    pub fn is_recurring(&self) -> bool {
        matches!(self, Self::Cron(_))
    }
}

impl fmt::Display for Schedule {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Cron(schedule) => schedule.fmt(formatter),
            Self::Once(instant) => formatter.write_str(&format_instant(*instant)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::Schedule;
    use crate::cron_schedule::CronSchedule;
    use crate::instant::{format_instant, parse_instant};
    use crate::offset::UtcOffset;

    fn instant(value: &str) -> std::time::SystemTime {
        parse_instant(value).unwrap()
    }

    fn cron(expression: &str) -> Schedule {
        Schedule::Cron(CronSchedule::new(expression, UtcOffset::UTC).unwrap())
    }

    #[test]
    fn fires_a_one_time_schedule_once() {
        let sut = Schedule::Once(instant("2026-09-10T03:00:00Z"));

        let next = sut.next_after(instant("2026-09-09T00:00:00Z")).unwrap();

        assert_eq!(format_instant(next), "2026-09-10T03:00:00Z");
        assert_eq!(sut.next_after(next), None, "the schedule already fired");
    }

    #[test]
    fn fires_a_cron_schedule_again() {
        let sut = cron("0 3 * * *");

        let next = sut.next_after(instant("2026-09-09T00:00:00Z")).unwrap();

        assert_eq!(format_instant(next), "2026-09-09T03:00:00Z");
        assert_eq!(
            format_instant(sut.next_after(next).unwrap()),
            "2026-09-10T03:00:00Z"
        );
    }

    #[test]
    fn reports_which_schedules_repeat() {
        assert!(cron("0 3 * * *").is_recurring());
        assert!(!Schedule::Once(instant("2026-09-10T03:00:00Z")).is_recurring());
    }

    #[test]
    fn writes_the_expression_or_the_instant() {
        assert_eq!(cron("0 3 * * *").to_string(), "0 3 * * *");
        assert_eq!(
            Schedule::Once(instant("2026-09-10T03:00:00Z")).to_string(),
            "2026-09-10T03:00:00Z"
        );
    }
}
