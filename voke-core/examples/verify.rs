use nucleo_matcher::{Config, Matcher};
use voke_core::{SearchKeys, score_entry};

fn main() {
    let mut m = Matcher::new(Config::DEFAULT);
    let names = ["微信", "网易云音乐", "QQ音乐", "Visual Studio Code", "终端"];
    for n in names {
        let k = SearchKeys::from_name(n);
        println!("{n} -> full={:?} initials={:?}", k.full, k.initials);
    }
    println!("\n=== 查询打分矩阵（验证排序是否符合直觉）===");
    let queries = ["wx", "weixin", "微信", "wyyy", "vscode", "vs", "yy", "zd"];
    print!("{:<8}", "");
    for q in queries {
        print!("{:<8}", q);
    }
    println!();
    for n in names {
        let k = SearchKeys::from_name(n);
        print!("{:<8}", n);
        for q in queries {
            let s = score_entry(&mut m, q, &k).map(|x| x.score).unwrap_or(0.0);
            print!("{:<8.3}", s);
        }
        println!();
    }
}
