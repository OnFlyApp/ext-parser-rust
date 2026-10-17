//! Crate docs.
use std::io::{self, Read};

/// A point.
#[derive(Debug, Clone)]
pub struct Point<T> {
    pub x: T,
    y: T,
}

pub struct Unit(i32);

pub enum Shape {
    Dot,
    Circle(f64),
}

pub union Bits {
    a: u32,
}

pub const MAX: usize = 10;
static mut COUNTER: i32 = 0;
type Id = u64;

macro_rules! twice {
    ($e:expr) => { $e * 2 };
}

/// Things with an area.
pub trait Area: Send {
    const SIDES: u32;
    type Unit;

    fn area(&self) -> f64;

    /// Default body.
    fn describe(&self) -> String {
        format!("{}", self.area())
    }
}

impl<T> Point<T> {
    /// Build one.
    pub async fn new(x: T, y: T) -> Self {
        Point { x, y }
    }
}

impl Area for Shape {
    const SIDES: u32 = 0;
    type Unit = f64;

    fn area(&self) -> f64 {
        0.0
    }
}

pub mod geometry {
    #[inline]
    pub fn dist(a: f64, b: f64) -> f64 {
        (a - b).abs()
    }
}

mod ext;

#[cfg(test)]
mod tests {
    #[test]
    fn it_works() {}
}

pub unsafe extern "C" fn ffi(a: i32) -> i32 {
    a
}

fn main() {
    println!("hi");
}
